import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync, readFileSync } from 'node:fs'
import { basename, join } from 'node:path'
import { expect } from '@playwright/test'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { uniqueMarker } from '../helpers/shellArguments'
import { sendMessage, waitForAgentIdle, waitForControlBanner } from '../helpers/ui'

/** Prove the selected native ZCode mode through its actual mutation and permission path. */
export async function exerciseZCodeMode(context: ManagedNativeScenarioContext, mode: 'plan' | 'yolo' | 'build'): Promise<void> {
  const agent = await currentNativeAgent(context)
  if (!agent.workingDir)
    throw new Error('The native ZCode mode proof requires a working directory.')
  expect(agent.optionGroups.find(group => group.id === 'permissionMode')?.currentValue).toBe(mode)
  const suffix = uniqueMarker()
  const path = join(agent.workingDir, `zcode-mode-write-${suffix}.txt`)
  const callId = `zcode-${mode}-write-${suffix}`
  const content = `${mode} mutation\n`
  const start = (await context.modelScript.status()).stepCount
  await context.modelScript.queue(
    { toolCalls: [writeToolCall(context.provider, callId, { path, content })] },
    { text: `The ${mode} check ended.` },
  )
  await sendMessage(context.page, context.modelScript.prompt(`Try the scripted write in ${mode} mode.`))
  if (mode === 'build') {
    await context.modelScript.waitForSteps(start + 1)
    const banner = await waitForControlBanner(context.page)
    await expect(banner).toContainText(basename(path))
    await context.page.getByTestId('control-deny-btn').filter({ visible: true }).click()
  }
  const status = await context.modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(context.page)
  await expect(context.page.locator('[data-testid="control-banner"]:visible')).toHaveCount(0)
  const result = nativeToolResult(status.requests.find(request => request.stepIndex === start + 1), callId)
  if (mode === 'yolo') {
    expect(readFileSync(path, 'utf8')).toBe(content)
    expect(result).toContain(basename(path))
  }
  else {
    expect(existsSync(path)).toBe(false)
    if (mode === 'plan')
      expect(result).toMatch(/plan|not available|denied/i)
  }
}
