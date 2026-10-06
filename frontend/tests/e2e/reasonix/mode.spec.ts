import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { nativeToolResultAt } from '../helpers/nativeToolExecution'
import { writeToolCall } from '../helpers/providerToolCalls'
import { answerControl, chooseSettingsOption, expectNoControlBanner, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixSessionSettings } from './settingsScenario'

reasonixTest('mode: applies Reasonix session settings and preserves them after reload', async ({ native }) => {
  await exerciseReasonixSessionSettings(native)
})

reasonixTest('refuses a native write in Plan mode and asks in Normal mode', async ({ native, authenticatedReasonixWorkspace }) => {
  const { page, modelScript } = native
  const workingDir = authenticatedReasonixWorkspace.workingDir
  for (const phase of ['before-reload', 'after-reload']) {
    const planFile = join(workingDir, `reasonix-plan-write-${phase}.txt`)
    const normalFile = join(workingDir, `reasonix-normal-write-${phase}.txt`)
    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    if (phase === 'after-reload') {
      await page.reload()
      await expectSettingsOptionChosen(page, 'permissionMode-plan')
    }
    const planCall = `reasonix-plan-write-${phase}`
    const planStep = await modelScript.queue(
      { toolCalls: [writeToolCall(AgentProvider.REASONIX, planCall, { path: planFile, content: 'plan mutation\n' })] },
      { text: 'The Plan check ended.' },
    )
    await sendMessage(page, modelScript.prompt('Try the scripted write in Plan mode.'))
    await modelScript.waitForSteps(planStep + 2)
    await waitForAgentIdle(page)
    expect(existsSync(planFile)).toBe(false)
    expect(await nativeToolResultAt(modelScript, planStep + 1, planCall)).toContain('plan mode forbids workspace mutations')
    const exitBanner = await waitForControlBanner(page)
    await expect(exitBanner).toContainText('exit_plan_mode')
    await answerControl(page, 'deny')
    await expectNoControlBanner(page)
    await chooseSettingsOption(page, 'permissionMode-normal')
    await chooseSettingsOption(page, 'tool_approval-ask')
    await waitForSettingsIdle(page)
    if (phase === 'after-reload') {
      await page.reload()
      await expectSettingsOptionChosen(page, 'permissionMode-normal')
      await expectSettingsOptionChosen(page, 'tool_approval-ask')
    }
    await exerciseNativePermissionDecision(native, {
      toolCall: writeToolCall(AgentProvider.REASONIX, `reasonix-normal-write-${phase}`, { path: normalFile, content: 'normal mutation\n' }),
      decision: 'deny',
      beforeDecision: banner => expect(banner).toContainText(`reasonix-normal-write-${phase}.txt`),
      nativeProof: () => expect(existsSync(normalFile)).toBe(false),
    })
  }
})
