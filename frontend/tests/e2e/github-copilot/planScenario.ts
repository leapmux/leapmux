import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { COPILOT_MODE, COPILOT_OPTION } from '../../../src/generated/contracts/copilot-protocol'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { answerPlanReview, assistantBubbles, chooseSettingsOption, expectNoControlBanner, expectSettingsChip, savedControlAnswer, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'

/**
 * Prove the plan approval of Copilot's Plan session mode:
 *
 * - Select the Plan session mode.
 * - Require the native `exit_plan_mode` tool in the model request.
 * - Approve the plan that the tool raises.
 * - Require that the turn resumes, and that the saved answer survives a reload.
 */
export async function exerciseCopilotPlanApproval(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  await chooseSettingsOption(page, `${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Plan}`)
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Plan')

  const first = await sendNativeAnswer(context, 'Confirm the selected mode.', 'Plan mode is active.')
  expect(JSON.stringify(first.body)).toContain('"name":"exit_plan_mode"')

  const start = await modelScript.queue(
    { toolCalls: [exitPlanModeToolCall(context.provider, 'copilot-plan', 'Review the Copilot change.')] },
    { text: 'The Copilot plan was approved.' },
  )
  await sendMessage(page, modelScript.prompt('Present the plan for approval.'))
  await modelScript.waitForSteps(start + 1)

  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('Proposed Plan')
  await expect(banner).toContainText('Review the Copilot change.')
  await answerPlanReview(page, 'approve')

  await modelScript.waitForSteps(start + 2)
  await waitForAgentIdle(page)
  await expectNoControlBanner(page)
  await expect(assistantBubbles(page).filter({ hasText: 'The Copilot plan was approved.' }).first()).toBeVisible()
  await page.reload()
  await expect(assistantBubbles(page).filter({ hasText: 'The Copilot plan was approved.' }).first()).toBeVisible()
  // The saved row proves the stored decision.
  await expect(savedControlAnswer(page)).toHaveText('Approve')
}
