import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('plan-mode: refuses a native write in Plan mode and asks in Normal mode', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
  const workingDir = authenticatedReasonixWorkspace.workingDir
  if (!workingDir)
    throw new Error('the Reasonix workspace has no working directory')
  const planFile = join(workingDir, 'reasonix-plan-write.txt')
  const normalFile = join(workingDir, 'reasonix-normal-write.txt')

  await chooseSettingsOption(page, 'permissionMode-plan')
  await waitForSettingsIdle(page)
  await modelScript.queue(
    { toolCalls: [writeToolCall(AgentProvider.REASONIX, 'reasonix-plan-write', { path: planFile, content: 'plan mutation\n' })] },
    { text: 'The Plan check ended.' },
  )
  await sendMessage(page, modelScript.prompt('Try the scripted write in Plan mode.'))
  const planned = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  expect(existsSync(planFile)).toBe(false)
  const planResult = nativeToolResult(planned.requests.find(request => request.stepIndex === 1), 'reasonix-plan-write')
  expect(planResult).toContain('plan mode forbids workspace mutations')
  const exitBanner = await waitForControlBanner(page)
  await expect(exitBanner).toContainText('exit_plan_mode')
  await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
  await expect(exitBanner).toHaveCount(0)

  await chooseSettingsOption(page, 'permissionMode-normal')
  await chooseSettingsOption(page, 'tool_approval-ask')
  await waitForSettingsIdle(page)
  await modelScript.queue(
    { toolCalls: [writeToolCall(AgentProvider.REASONIX, 'reasonix-normal-write', { path: normalFile, content: 'normal mutation\n' })] },
    { text: 'The Normal check ended.' },
  )
  await sendMessage(page, modelScript.prompt('Try the scripted write in Normal mode.'))
  await modelScript.waitForSteps(3)
  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('reasonix-normal-write.txt')
  await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
  await modelScript.waitForSteps(4)
  await waitForAgentIdle(page)
  expect(existsSync(normalFile)).toBe(false)
})
