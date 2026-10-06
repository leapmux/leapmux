import { expect } from '@playwright/test'
import { PI_DIALOG_METHOD, PI_EVENT, PI_PLAN_ACTION, PI_PLAN_DIALOG } from '../../../src/generated/contracts/pi-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { pickString } from '../../../src/lib/jsonPick'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { openAgentViaAPI } from '../helpers/api'
import { withCleanup } from '../helpers/cleanup'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent, nativeAgentById, nativeModelInstructionText } from '../helpers/nativeScenario'
import { onlyObservedNativeControl, readNativeStoredControlDecision } from '../helpers/nativeStoredControlDecision'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { controlBanner, openWorkspace, savedControlAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'

piTest('plan-approval-banner: tracks a fresh Pi implementation session after plan approval', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const provider = AgentProvider.PI
  const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-fresh-plan-'), {
    agentProvider: provider,
    ...agentOpenOptions(agentSettings(provider)),
  })
  const readSession = async () => (await nativeAgentById({ leapmuxServer }, agentId))?.agentSessionId ?? ''
  await expect.poll(readSession).not.toBe('')
  const originalSession = await readSession()
  expect(originalSession).not.toBe('')
  await page.reload()
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await sendMessage(page, '/plan start')
  // SCRIPTED, not asked for. The old prompt told the model to call
  // `plan_mode_complete` and hoped it would; against the mock nothing
  // answered, so the banner never appeared.
  // The MARKER rides inside the plan. Approving with clear-context starts a
  // FRESH session whose first prompt is the plan itself, and that turn carries
  // no marker of its own -- so without this the implementation turn would
  // reach the ambient scenario rather than this test's script.
  await modelScript.queue({
    toolCalls: [exitPlanModeToolCall(provider, 'fresh-plan', modelScript.prompt('# Fresh implementation probe\n\n- Reply with FRESH_PLAN_DONE. Do not call tools or change files.'))],
  })
  await modelScript.queue({ text: 'FRESH_PLAN_DONE' })
  await sendMessage(page, modelScript.prompt('Finish the plan.'))
  await modelScript.waitForSteps(1)
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText('Plan Ready for Review')
  await page.getByTestId('plan-clear-context-checkbox').filter({ visible: true }).click()
  await page.getByTestId('plan-approve-btn').click()
  await expect.poll(async () => {
    const current = await readSession()
    return current !== '' && current !== originalSession
  }).toBe(true)
  await expect(page.locator('[data-chat-scroll-container="true"]').filter({ visible: true }).getByText('FRESH_PLAN_DONE', { exact: true })).toBeVisible()
})

// The pi-plan-mode extension answers in two steps. `plan_mode_complete` returns
// `terminate: true`, so Pi ends the turn with no further model request. When the
// agent settles, the extension shows its review menu as an extension UI select. Reject
// selects "Stay in Plan mode", whose action does nothing: no turn starts, plan mode
// stays on, and the session stays the same. The next prompt therefore reaches the model
// with the plan-mode contract and without the Normal-mode contract that an exit adds.
piTest('rejects the native Pi plan review and keeps planning in the same session', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const provider = AgentProvider.PI
  const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-plan-stay-'), {
    agentProvider: provider,
    ...agentOpenOptions(agentSettings(provider)),
  })
  const context = { page, modelScript, provider, leapmuxServer }
  await expect.poll(async () => (await nativeAgentById(context, agentId))?.agentSessionId ?? '').not.toBe('')
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const agent = await currentNativeAgent(context)
  expect(agent.id).toBe(agentId)
  const watch = await watchNativeControls(leapmuxServer, agent.id)
  await withCleanup(async () => {
    await sendMessage(page, '/plan start')
    await modelScript.queue({
      toolCalls: [exitPlanModeToolCall(provider, 'stay-plan', '# Stay probe\n\n- Keep this plan unapproved.')],
    })
    await sendMessage(page, modelScript.prompt('Finish the plan.'))
    await modelScript.waitForSteps(1)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    await expect.poll(() => watch.controls().length).toBeGreaterThan(0)
    const observed = onlyObservedNativeControl(watch.controls())
    await page.getByTestId('plan-reject-btn').filter({ visible: true }).click()

    await expect(banner).toHaveCount(0)
    await expect(savedControlAnswer(page)).toHaveText('Rejected')
    await waitForAgentIdle(page)
    const status = await modelScript.status()
    expect(status.unexpectedRequests).toEqual([])
    expect(status.requests.filter(request => request.stepIndex !== undefined)).toHaveLength(1)
    expect(onlyObservedNativeControl(watch.controls())).toBe(observed)

    const snapshot = await readNativeMessageSnapshot(context, agent.id)
    expect(snapshot.agentSessionId).toBe(agent.agentSessionId)
    const decision = readNativeStoredControlDecision(snapshot, observed.requestId)
    expect(decision.request).toEqual(observed.payload)
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
    expect(onlyObservedNativeControl(watch.controls())).toBe(observed)

    await page.reload()
    await expect(savedControlAnswer(page)).toHaveText('Rejected')
  }, async () => watch.cancel())
})
