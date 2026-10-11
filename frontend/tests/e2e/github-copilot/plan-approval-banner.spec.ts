import { expect } from '@playwright/test'
import { COPILOT_EVENT, COPILOT_MODE, COPILOT_OPTION } from '../../../src/generated/contracts/copilot-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { pickObject, pickString } from '../../../src/lib/jsonPick'
import { copilotTest } from '../copilot-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { expectTurnEndedAfter } from '../helpers/modelScriptFixture'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { waitForNativeOptionApplied } from '../helpers/nativeSettings'
import { readObservedNativeDecision, waitForOneNativeControl } from '../helpers/nativeStoredControlDecision'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { answerPlanReview, chooseSettingsOption, controlBanner, expectSettingsChip, expectSettingsOptionChosen, savedControlAnswer, sendMessage, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'
import { copilotToolCompletion } from './permissionRefusal'
import { exerciseCopilotPlanApproval } from './planScenario'

copilotTest('plan-approval-banner: uses the native exit tool and resumes after plan approval', async ({ native }) => {
  await exerciseCopilotPlanApproval(native)
})

// A Reject that carries no feedback answers `{ approved: false }`. The runtime then
// fails the exit_plan_mode call with the code `rejected` and ends the turn with no
// further model request. The model reads that result with the next prompt, and the
// session stays in plan mode. A Copilot session log of runtime 1.0.83 records this
// sequence, and runtime 1.0.87 holds the same result text. Feedback is different:
// it gives the model a successful result, and the turn goes on.
copilotTest('plan-approval-banner: rejects the native exit tool, ends the turn, and stays in plan mode', async ({ native }) => {
  const { page, modelScript, leapmuxServer } = native
  await chooseSettingsOption(page, `${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Plan}`)
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Plan')
  const agent = await waitForNativeOptionApplied(native, COPILOT_OPTION.SessionMode, COPILOT_MODE.Plan)
  const watch = await watchNativeControls(leapmuxServer, agent.id)
  await withCleanup(async () => {
    const callId = 'copilot-rejected-plan'
    const start = await modelScript.queue({
      toolCalls: [exitPlanModeToolCall(AgentProvider.GITHUB_COPILOT, callId, 'Keep the Copilot plan unapproved.')],
    })
    await sendMessage(page, modelScript.prompt('Present the plan for approval.'))
    await modelScript.waitForSteps(start + 1)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Proposed Plan')
    await expect(banner).toContainText('Keep the Copilot plan unapproved.')
    const observed = await waitForOneNativeControl(watch)
    await answerPlanReview(page, 'reject')

    await expect(banner).toHaveCount(0)
    await expect(savedControlAnswer(page)).toHaveText('Reject')
    await waitForAgentIdle(page)
    await expectSettingsOptionChosen(page, `${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Plan}`)
    await expectTurnEndedAfter(modelScript, start + 1)

    const { decision, snapshot } = await readObservedNativeDecision(native, agent, watch, observed)
    const request = pickObject(pickObject(decision.request, 'params'), 'event')
    expect(pickString(request, 'type')).toBe(COPILOT_EVENT.ExitPlanModeRequested)
    expect(pickString(pickObject(request, 'data'), 'summary')).toBe('Keep the Copilot plan unapproved.')
    // The answer has no feedback field, which is what makes the runtime end the turn.
    expect(decision.response).toEqual({
      type: 'control_response',
      response: { subtype: 'success', request_id: observed.requestId, response: { approved: false } },
    })

    // Copilot keeps the call ID of the model, and the root session holds exactly one completion of that call.
    const completion = copilotToolCompletion(snapshot, callId)
    expect(completion, 'Copilot failed the exit tool call of the rejected plan').toMatchObject({ success: false, error: { code: 'rejected' } })
    expect(completion.error?.message).toContain('User requested changes but did not provide specific feedback.')

    await page.reload()
    await expect(savedControlAnswer(page)).toHaveText('Reject')
    await expectSettingsChip(page, 'Plan')
  }, async () => watch.cancel())
})
