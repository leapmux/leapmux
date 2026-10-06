import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { writeToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, expectNoControlBanner, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixSessionSettings } from './settingsScenario'

reasonixTest('mode: applies Reasonix session settings and preserves them after reload', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseReasonixSessionSettings({ page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX })
})

reasonixTest('refuses a native write in Plan mode and asks in Normal mode', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
  const workingDir = authenticatedReasonixWorkspace.workingDir
  if (!workingDir)
    throw new Error('The Reasonix workspace has no working directory.')
  for (const phase of ['before-reload', 'after-reload']) {
    const planFile = join(workingDir, `reasonix-plan-write-${phase}.txt`)
    const normalFile = join(workingDir, `reasonix-normal-write-${phase}.txt`)
    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    if (phase === 'after-reload') {
      await page.reload()
      await expectSettingsOptionChosen(page, 'permissionMode-plan')
    }
    const start = (await modelScript.status()).stepCount
    const planCall = `reasonix-plan-write-${phase}`
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.REASONIX, planCall, { path: planFile, content: 'plan mutation\n' })] },
      { text: 'The Plan check ended.' },
    )
    await sendMessage(page, modelScript.prompt('Try the scripted write in Plan mode.'))
    const planned = await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    expect(existsSync(planFile)).toBe(false)
    const planResult = nativeToolResult(planned.requests.find(request => request.stepIndex === start + 1), planCall)
    expect(planResult).toContain('plan mode forbids workspace mutations')
    const exitBanner = await waitForControlBanner(page)
    await expect(exitBanner).toContainText('exit_plan_mode')
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await expectNoControlBanner(page)
    await chooseSettingsOption(page, 'permissionMode-normal')
    await chooseSettingsOption(page, 'tool_approval-ask')
    await waitForSettingsIdle(page)
    if (phase === 'after-reload') {
      await page.reload()
      await expectSettingsOptionChosen(page, 'permissionMode-normal')
      await expectSettingsOptionChosen(page, 'tool_approval-ask')
    }
    const normalCall = `reasonix-normal-write-${phase}`
    await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.REASONIX, normalCall, { path: normalFile, content: 'normal mutation\n' })] },
      { text: 'The Normal check ended.' },
    )
    await sendMessage(page, modelScript.prompt('Try the scripted write in Normal mode.'))
    await modelScript.waitForSteps(start + 3)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText(`reasonix-normal-write-${phase}.txt`)
    await page.getByTestId('control-deny-btn').filter({ visible: true }).click()
    await modelScript.waitForSteps(start + 4)
    await waitForAgentIdle(page)
    expect(existsSync(normalFile)).toBe(false)
  }
})
