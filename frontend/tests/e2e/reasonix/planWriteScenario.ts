import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { currentNativeAgent, nativeTextStep } from '../helpers/nativeScenario'
import { nativeToolResultAt } from '../helpers/nativeToolExecution'
import { writeToolCall } from '../helpers/providerToolCalls'
import { answerControl, chooseSettingsOption, expectNoControlBanner, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'

/**
 * Prove that Reasonix refuses a native write in Plan mode and asks before the same write in Normal mode, before and
 * after a reload.
 *
 * Reasonix 1.38 ends each Plan-mode answer with an `exit_plan_mode` permission request, so the scenario refuses that
 * request and keeps Plan mode. In Normal mode with the Ask approval, the write raises a permission request, and the
 * scenario refuses it through `exerciseNativePermissionDecision`, so no file exists after either write.
 */
export async function exerciseReasonixPlanAndNormalWrites(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  const { workingDir } = await currentNativeAgent(context)
  if (!workingDir)
    throw new Error('The Reasonix write proof requires the working directory of the agent.')
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
    const planned = await modelScript.queue(
      { toolCalls: [writeToolCall(context.provider, planCall, { path: planFile, content: 'plan mutation\n' })] },
      nativeTextStep(context, 'The Plan check ended.'),
    )
    await sendMessage(page, modelScript.prompt('Try the scripted write in Plan mode.'))
    await modelScript.waitForSteps(planned + 2)
    await waitForAgentIdle(page)
    expect(existsSync(planFile)).toBe(false)
    expect(await nativeToolResultAt(modelScript, planned + 1, planCall)).toContain('plan mode forbids workspace mutations')
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
    await exerciseNativePermissionDecision(context, {
      toolCall: writeToolCall(context.provider, `reasonix-normal-write-${phase}`, { path: normalFile, content: 'normal mutation\n' }),
      decision: 'deny',
      beforeDecision: banner => expect(banner).toContainText(`reasonix-normal-write-${phase}.txt`),
      nativeProof: () => expect(existsSync(normalFile)).toBe(false),
    })
  }
}
