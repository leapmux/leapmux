import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { bashToolCall, goosePermissionJudgmentToolCall } from '../helpers/providerToolCalls'
import { applyPermissionPreset, sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

/** Deny a real removal through Smart, then execute that removal through Auto. */
export async function exerciseGoosePermissionRemoval(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  const workingDir = (await currentNativeAgent(context)).workingDir
  if (!workingDir)
    throw new Error('the Goose workspace has no working directory')
  await applyPermissionPreset(page, 'bypass')
  await applyPermissionPreset(page, 'smart')
  expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'permissionMode')?.currentValue).toBe('smart_approve')
  const start = (await modelScript.status()).stepCount
  await modelScript.rule({ name: `goose-native-removal-judge-${start}`, when: { system: 'permission-safety classifier' }, respond: { toolCalls: [goosePermissionJudgmentToolCall('goose-removal-judge', [])] } })
  const marker = join(workingDir, 'goose-mode-marker.txt')
  writeFileSync(marker, 'keep this file\n')

  await modelScript.queue(
    { toolCalls: [bashToolCall(AgentProvider.GOOSE, 'goose-smart-remove', 'rm -f goose-mode-marker.txt && printf goose-mode-42')] },
    { text: 'The Smart check ended.' },
  )
  await sendMessage(page, modelScript.prompt('Try the scripted removal under Smart Approve.'))
  await modelScript.waitForSteps(start + 1)
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('goose-mode-marker.txt')
  await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  expect(existsSync(marker)).toBe(true)

  await applyPermissionPreset(page, 'bypass')
  expect((await currentNativeAgent(context)).optionGroups.find(group => group.id === 'permissionMode')?.currentValue).toBe('auto')
  await expectNoNativeControl(context, { testId: 'control-banner', relatedControl: async () => {
    await modelScript.queue(
      { toolCalls: [bashToolCall(AgentProvider.GOOSE, 'goose-auto-remove', 'rm -f goose-mode-marker.txt && printf goose-mode-42')] },
      { text: 'The Auto check ended.' },
    )
    await sendMessage(page, modelScript.prompt('Run the scripted removal under Auto.'))
    const status = await modelScript.waitForSteps(start + 4)
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    expect(existsSync(marker)).toBe(false)
    const result = nativeToolResult(status.requests.find(request => request.stepIndex === start + 3), 'goose-auto-remove')
    expect(result).toContain('goose-mode-42')
  } })
}
