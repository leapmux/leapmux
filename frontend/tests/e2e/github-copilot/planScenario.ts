import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { COPILOT_MODE, COPILOT_OPTION } from '../../../src/generated/contracts/copilot-protocol'
import { pickObject } from '../../../src/lib/jsonPick'
import { withCleanup } from '../helpers/cleanup'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { waitForNativeOptionApplied } from '../helpers/nativeSettings'
import { readObservedNativeDecision, waitForOneNativeControl } from '../helpers/nativeStoredControlDecision'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { answerPlanReview, assistantBubbles, chooseSettingsOption, expectNoControlBanner, expectSettingsChip, savedControlAnswer, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'
import { copilotToolCompletion } from './permissionRefusal'

/**
 * Prove the plan approval of Copilot's Plan session mode:
 *
 * - Select the Plan session mode.
 * - Require the native `exit_plan_mode` tool in the model request.
 * - Approve the plan that the tool raises.
 * - Require the native answer: the Worker delivered `approved: true` with no feedback, and Copilot completed its
 *   `exit_plan_mode` call with success.
 * - Require that the turn resumes, and that the saved answer survives a reload.
 */
export async function exerciseCopilotPlanApproval(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript, leapmuxServer } = context
  await chooseSettingsOption(page, `${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Plan}`)
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Plan')
  const agent = await waitForNativeOptionApplied(context, COPILOT_OPTION.SessionMode, COPILOT_MODE.Plan)

  const first = await sendNativeAnswer(context, 'Confirm the selected mode.', 'Plan mode is active.')
  expect(JSON.stringify(first.body)).toContain('"name":"exit_plan_mode"')

  const callId = 'copilot-plan'
  // The script writes this answer itself, so the answer proves only that the turn resumed, not how the user answered.
  const resumed = 'The Copilot turn resumed after the plan review.'
  const watch = await watchNativeControls(leapmuxServer, agent.id)
  await withCleanup(async () => {
    const start = await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(context.provider, callId, 'Review the Copilot change.')] },
      { text: resumed },
    )
    await sendMessage(page, modelScript.prompt('Present the plan for approval.'))
    await modelScript.waitForSteps(start + 1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Proposed Plan')
    await expect(banner).toContainText('Review the Copilot change.')
    const observed = await waitForOneNativeControl(watch)
    await answerPlanReview(page, 'approve')

    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expectNoControlBanner(page)
    await expect(assistantBubbles(page).filter({ hasText: resumed })).toBeVisible()

    const { decision, snapshot } = await readObservedNativeDecision(context, agent, watch, observed)
    expect(decision.response).toMatchObject({ type: 'control_response', response: { subtype: 'success', request_id: observed.requestId } })
    const answer = pickObject(pickObject(decision.response, 'response'), 'response')
    expect(answer, 'the Worker delivered an approval').toMatchObject({ approved: true })
    // An answer with feedback asks Copilot for changes to the plan, so an approval carries none.
    expect(answer, 'the approval carries no feedback').not.toHaveProperty('feedback')
    expect(copilotToolCompletion(snapshot, callId), 'Copilot completed the exit tool call of the approved plan').toEqual({ success: true })

    await page.reload()
    await expect(assistantBubbles(page).filter({ hasText: resumed })).toBeVisible()
    await expect(savedControlAnswer(page)).toHaveText('Approve')
  }, async () => watch.cancel())
}
