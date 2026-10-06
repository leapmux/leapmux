import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { expect } from '@playwright/test'
import { COPILOT_EVENT, COPILOT_METHOD, COPILOT_MODE, COPILOT_OPTION, COPILOT_TOOL } from '../../../src/generated/contracts/copilot-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickObject, pickString } from '../../../src/lib/jsonPick'
import { copilotTest } from '../copilot-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { attachCopilotNativeLogs } from '../helpers/copilotNativeLogs'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { nativeMessageBody, readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { waitForNativeOptionApplied } from '../helpers/nativeSettings'
import { onlyObservedNativeControl, readNativeStoredControlDecision } from '../helpers/nativeStoredControlDecision'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, chooseSettingsOption, controlBanner, expectNoControlBanner, expectSettingsChip, expectSettingsOptionChosen, messageBubbles, savedControlAnswer, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from '../helpers/ui'

copilotTest('plan-approval-banner: uses the native exit tool and resumes after plan approval', async ({ authenticatedCopilotWorkspace, leapmuxServer, page, modelScript }, testInfo) => {
  void authenticatedCopilotWorkspace
  await chooseSettingsOption(page, `${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Plan}`)
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Plan')

  await modelScript.queue({ text: 'Plan mode is active.' })
  await sendMessage(page, modelScript.prompt('Confirm the selected mode.'))
  const first = await modelScript.waitForSteps(1)
  await waitForAgentIdle(page)
  expect(JSON.stringify(first.requests.find(request => request.stepIndex === 0)?.body)).toContain('"name":"exit_plan_mode"')

  await modelScript.queue(
    { toolCalls: [exitPlanModeToolCall(AgentProvider.GITHUB_COPILOT, 'copilot-plan', 'Review the Copilot change.')] },
    { text: 'The Copilot plan was approved.' },
  )
  await sendMessage(page, modelScript.prompt('Present the plan for approval.'))
  await modelScript.waitForSteps(2)
  await attachCopilotNativeLogs(leapmuxServer.agentEnv.COPILOT_HOME, testInfo)

  const banner = await waitForControlBanner(page)
  await expect(banner).toContainText('Proposed Plan')
  await expect(banner).toContainText('Review the Copilot change.')
  await expect(page.getByTestId('plan-approve-btn').filter({ visible: true })).toBeVisible()
  await page.getByTestId('plan-approve-btn').filter({ visible: true }).click()

  await modelScript.waitForSteps(3)
  await waitForAgentIdle(page)
  await expectNoControlBanner(page)
  await expect(assistantBubbles(page).filter({ hasText: 'The Copilot plan was approved.' }).first()).toBeVisible()
  await page.reload()
  await expect(messageBubbles(page).filter({ hasText: 'Approved' }).first()).toBeVisible()
  // The bubble filter above also matches the assistant text "…was approved." The saved row proves the stored decision.
  await expect(savedControlAnswer(page)).toHaveText('Approve')
})

/** One session event of the root agent, as the Worker stored the native frame. */
interface CopilotRootEvent {
  type: string
  data: Record<string, unknown>
}

/** Read the root agent's native session events of one session, in Worker order. */
function copilotRootEvents(snapshot: NativeMessageSnapshot): CopilotRootEvent[] {
  return snapshot.messages.flatMap((message) => {
    const frame = nativeMessageBody(message)
    if (!isObject(frame) || pickString(frame, 'method') !== COPILOT_METHOD.SessionEvent)
      return []
    const params = pickObject(frame, 'params')
    const event = pickObject(params, 'event')
    const data = pickObject(event, 'data')
    // A subagent's event states its own agentId. The root agent's event states none.
    if (pickString(params, 'sessionId') !== snapshot.agentSessionId || !event || !data || pickString(event, 'agentId') !== '')
      return []
    return [{ type: pickString(event, 'type'), data }]
  })
}

// A Reject that carries no feedback answers `{ approved: false }`. The runtime then
// fails the exit_plan_mode call with the code `rejected` and ends the turn with no
// further model request. The model reads that result with the next prompt, and the
// session stays in plan mode. A Copilot session log of runtime 1.0.83 records this
// sequence, and runtime 1.0.87 holds the same result text. Feedback is different:
// it gives the model a successful result, and the turn goes on.
copilotTest('plan-approval-banner: rejects the native exit tool, ends the turn, and stays in plan mode', async ({ authenticatedCopilotWorkspace, leapmuxServer, page, modelScript }, testInfo) => {
  void authenticatedCopilotWorkspace
  await chooseSettingsOption(page, `${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Plan}`)
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Plan')
  const agent = await waitForNativeOptionApplied({ page, leapmuxServer }, COPILOT_OPTION.SessionMode, COPILOT_MODE.Plan)
  const watch = await watchNativeControls(leapmuxServer, agent.id)
  await withCleanup(async () => {
    await modelScript.queue({
      toolCalls: [exitPlanModeToolCall(AgentProvider.GITHUB_COPILOT, 'copilot-rejected-plan', 'Keep the Copilot plan unapproved.')],
    })
    await sendMessage(page, modelScript.prompt('Present the plan for approval.'))
    await modelScript.waitForSteps(1)
    await attachCopilotNativeLogs(leapmuxServer.agentEnv.COPILOT_HOME, testInfo)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Proposed Plan')
    await expect(banner).toContainText('Keep the Copilot plan unapproved.')
    await expect.poll(() => watch.controls().length).toBeGreaterThan(0)
    const observed = onlyObservedNativeControl(watch.controls())
    await page.getByTestId('plan-reject-btn').filter({ visible: true }).click()

    await expect(banner).toHaveCount(0)
    await expect(savedControlAnswer(page)).toHaveText('Reject')
    await waitForAgentIdle(page)
    await expectSettingsOptionChosen(page, `${COPILOT_OPTION.SessionMode}-${COPILOT_MODE.Plan}`)
    const status = await modelScript.status()
    expect(status.unexpectedRequests).toEqual([])
    expect(status.requests.filter(request => request.stepIndex !== undefined)).toHaveLength(1)
    expect(onlyObservedNativeControl(watch.controls())).toBe(observed)

    const snapshot = await readNativeMessageSnapshot({ leapmuxServer }, agent.id)
    expect(snapshot.agentSessionId).toBe(agent.agentSessionId)
    const decision = readNativeStoredControlDecision(snapshot, observed.requestId)
    expect(decision.request).toEqual(observed.payload)
    const request = pickObject(pickObject(decision.request, 'params'), 'event')
    expect(pickString(request, 'type')).toBe(COPILOT_EVENT.ExitPlanModeRequested)
    expect(pickString(pickObject(request, 'data'), 'summary')).toBe('Keep the Copilot plan unapproved.')
    // The answer has no feedback field, which is what makes the runtime end the turn.
    expect(decision.response).toEqual({
      type: 'control_response',
      response: { subtype: 'success', request_id: observed.requestId, response: { approved: false } },
    })

    const events = copilotRootEvents(snapshot)
    const starts = events.filter(event => event.type === COPILOT_EVENT.ToolStarted && event.data.toolName === COPILOT_TOOL.ExitPlanMode)
    expect(starts).toHaveLength(1)
    const callId = pickString(starts[0]?.data, 'toolCallId')
    expect(callId).not.toBe('')
    const results = events.filter(event => event.type === COPILOT_EVENT.ToolCompleted && event.data.toolCallId === callId)
    expect(results).toHaveLength(1)
    const result = results[0]?.data
    expect(result).toMatchObject({ success: false, error: { code: 'rejected' } })
    expect(pickString(pickObject(result, 'error'), 'message')).toContain('User requested changes but did not provide specific feedback.')

    await page.reload()
    await expect(savedControlAnswer(page)).toHaveText('Reject')
    await expectSettingsChip(page, 'Plan')
  }, async () => watch.cancel())
})
