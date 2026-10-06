import { expect } from '@playwright/test'
import { PI_DIALOG_METHOD, PI_EVENT, PI_PLAN_ACTION, PI_PLAN_DIALOG } from '../../../src/generated/contracts/pi-protocol'
import { pickString } from '../../../src/lib/jsonPick'
import { agentOpenOptions } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { withCleanup } from '../helpers/cleanup'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { currentNativeAgent, nativeAgentById, nativeModelInstructionText } from '../helpers/nativeScenario'
import { expectTurnEndedAfter, onlyObservedNativeControl, readObservedNativeDecision, waitForOneNativeControl } from '../helpers/nativeStoredControlDecision'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { createTestDirectory } from '../helpers/runDirectory'
import { answerPlanReview, controlBanner, openWorkspace, savedControlAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'
import { exercisePiFreshPlanSession } from './planScenario'
import { nativeContext } from './scenarios'

piTest('plan-approval-banner: tracks a fresh Pi implementation session after plan approval', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  await exercisePiFreshPlanSession(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId }))
})

// The pi-plan-mode extension answers in two steps. `plan_mode_complete` returns
// `terminate: true`, so Pi ends the turn with no further model request. When the
// agent settles, the extension shows its review menu as an extension UI select. Reject
// selects "Stay in Plan mode", whose action does nothing: no turn starts, plan mode
// stays on, and the session stays the same. The next prompt therefore reaches the model
// with the plan-mode contract and without the Normal-mode contract that an exit adds.
piTest('rejects the native Pi plan review and keeps planning in the same session', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, context.workspaceId, createTestDirectory('renderer-pi-plan-stay-'), agentOpenOptions(context.provider))
  await retryUntilPass(async () => {
    expect((await nativeAgentById(context, agentId))?.agentSessionId ?? '', 'the Worker starts the native session of the new agent').not.toBe('')
  })
  await openWorkspace(page, context.workspaceId)
  const agent = await currentNativeAgent(context)
  expect(agent.id).toBe(agentId)
  const watch = await watchNativeControls(leapmuxServer, agent.id)
  await withCleanup(async () => {
    await sendMessage(page, '/plan start')
    const start = await modelScript.queue({
      toolCalls: [exitPlanModeToolCall(context.provider, 'stay-plan', '# Stay probe\n\n- Keep this plan unapproved.')],
    })
    await sendMessage(page, modelScript.prompt('Finish the plan.'))
    await modelScript.waitForSteps(start + 1)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    const observed = await waitForOneNativeControl(watch)
    await answerPlanReview(page, 'reject')

    await expect(banner).toHaveCount(0)
    await expect(savedControlAnswer(page)).toHaveText('Rejected')
    await waitForAgentIdle(page)
    await expectTurnEndedAfter(modelScript, start + 1)

    const { decision } = await readObservedNativeDecision(context, agent, watch, observed)
    // The Worker request ID is Pi's own dialog id, which the answer repeats.
    expect(decision.request).toMatchObject({ type: PI_EVENT.ExtensionUIRequest, id: observed.requestId, method: PI_DIALOG_METHOD.Select })
    expect(pickString(decision.request, 'title').split('\n', 1)[0]).toBe(PI_PLAN_DIALOG.ReadyTitle)
    expect(decision.request.options).toContain(PI_PLAN_ACTION.Stay)
    expect(decision.response).toEqual({ type: PI_EVENT.ExtensionUIResponse, id: observed.requestId, value: PI_PLAN_ACTION.Stay })

    const request = await sendNativeAnswer(context, 'Continue planning.', 'PI_PLAN_STAYED')
    const instructions = nativeModelInstructionText(request)
    expect(instructions).toContain('[CODEX-LIKE PLAN MODE ACTIVE]')
    expect(instructions).not.toContain('[PI PLAN MODE CONTRACT v1: NORMAL]')
    expect((await nativeAgentById(context, agent.id))?.agentSessionId).toBe(agent.agentSessionId)
    // The next turn raised no second review.
    expect(onlyObservedNativeControl(watch.controls())).toBe(observed)

    await page.reload()
    await expect(savedControlAnswer(page)).toHaveText('Rejected')
  }, async () => watch.cancel())
})
