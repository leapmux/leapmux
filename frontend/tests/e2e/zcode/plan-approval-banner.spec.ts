import { Buffer } from 'node:buffer'
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { ZCODE_ACTION, ZCODE_ANSWER_FIELD, ZCODE_METHOD, ZCODE_PLAN_CONTROL, ZCODE_REPLY_FIELD, ZCODE_TOOL } from '../../../src/generated/contracts/zcode-protocol'
import { AgentProvider, ControlResponseState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { withCleanup } from '../helpers/cleanup'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readObservedNativeDecision, waitForOneNativeControl } from '../helpers/nativeStoredControlDecision'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { answerPlanReview, assistantBubbles, controlActions, controlBanner, enterControlFeedback, expectSettingsChip, openWorkspace, savedControlAnswer, sendMessage, userBubbles, visibleOnly, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { ZCODE_AGENT, zcodeTest } from '../zcode-fixtures'

zcodeTest('rejects a native ZCode plan and delivers approval-shaped feedback as feedback', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.setViewportSize({ width: 780, height: 1000 })
  const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, ZCODE_AGENT, { workingDir: createTestDirectory('zcode-feedback-'), optionValues: { [OPTION_ID_PERMISSION_MODE]: 'plan' } })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await expectSettingsChip(page, 'Plan')
  // Two turns: the plan request, then the answer to the rejection feedback. The
  // SUBJECT is that approval-shaped feedback text ("approve") reaches the agent
  // as FEEDBACK and not as an approval, so both turns are scripted and the
  // control round trip is the only variable left.
  const start = await modelScript.queue(
    { toolCalls: [exitPlanModeToolCall(AgentProvider.ZCODE, 'feedback-probe', '# Feedback probe\n\n- Change no files.')] },
    { text: 'FEEDBACK_RECEIVED' },
  )
  await sendMessage(page, modelScript.prompt('Request approval for a short plan titled "Feedback probe".'))
  await modelScript.waitForSteps(start + 1)
  const banner = controlBanner(page)
  await expect(banner).toContainText('Plan Ready for Review')
  await enterControlFeedback(page, 'approve')
  const reject = page.getByTestId('plan-reject-btn').filter({ visible: true })
  await expect(reject).toHaveText('Send feedback')
  await expect(reject).toBeInViewport({ ratio: 1 })
  // No queue toggle, visible or hidden, while the composer holds the feedback of a control.
  await expect(page.getByTestId('queue-pause-button')).toHaveCount(0)
  await answerPlanReview(page, 'reject')
  await expect(banner).toHaveCount(0)
  await expect(assistantBubbles(page).filter({ hasText: 'FEEDBACK_RECEIVED' }).last()).toBeVisible()
  await waitForAgentIdle(page)
  // The FEEDBACK, not the bare label. `feedbackOrLabel` renders the reason when
  // a deny carries one and falls back to "Rejected" only when it does not, so a
  // deny WITH feedback shows the feedback -- which is this test's whole name.
  // Asserting "Rejected" here contradicted the rule the product documents.
  //
  // The model's reasoning can move the earlier response outside the rendered
  // message window.
  const rejection = userBubbles(page).getByText('approve', { exact: true })
  if (await rejection.count() === 0)
    await visibleOnly(page.getByRole('button', { name: 'Your response', exact: true })).first().click()
  await rejection.first().scrollIntoViewIfNeeded()
  await expect(rejection.first()).toBeInViewport({ ratio: 1 })
  // PLAN, not Build. ZCode asks for the plan approval as the permission check of
  // its ExitPlanMode tool, and the tool's handler, which leaves plan mode, runs
  // only after an approval. This test declines (the stored row below records
  // `action: decline`), so the handler never runs and the session stays in plan
  // mode for the model to revise the plan. ZCode keeps plan mode in a
  // `planEnabled` flag beside the native mode, and `settings.mode.current` reads
  // `build` throughout, so a chip that reads Build here follows that field instead
  // of the flag.
  await expectSettingsChip(page, 'Plan')
  const database = join(leapmuxServer.dataDir, 'worker', 'worker.db')
  const agent = `'${agentId.replaceAll('\'', '\'\'')}'`
  const rows = JSON.parse(execFileSync('sqlite3', ['-json', database, `SELECT state, feedback, hex(resolved_content) AS content FROM control_response_answers WHERE agent_id=${agent} AND feedback='approve'`], { encoding: 'utf8' }) || '[]') as Array<{ state: number, feedback: string, content: string }>
  expect(rows).toHaveLength(1)
  const [row] = rows
  if (row === undefined)
    throw new Error('expected exactly one stored approval feedback row')
  // The column stores the proto enum ORDINAL, not its name -- see "Enum columns
  // store proto enum ordinals" in AGENTS.md. Binding the generated constant
  // means a renumber propagates here instead of leaving a stale literal.
  expect(row.state).toBe(ControlResponseState.COMPLETED)
  expect(JSON.parse(Buffer.from(row.content, 'hex').toString()).result.action).toBe('decline')
  expect(errors).toEqual([])
})

/**
 * The file stem that ZCode gives the plan file of one session: each run of other
 * characters becomes `-`, and the outer `-` characters go (`$pa` in zcode.cjs 3.14.4).
 */
function zcodePlanFileStem(sessionId: string): string {
  return sessionId.trim().replace(/[^\w.-]+/g, '-').replace(/^-+|-+$/g, '')
}

// Approve answers ZCode's plan approval with `accept` and the approval word. ZCode
// then runs its ExitPlanMode handler, which runs for an approval alone:
// - It writes the plan to a plan file in the working directory.
// - It turns plan mode off.
// - It gives the model a result that approves the plan, so the model answers once
//   more in the same turn.
// ZCode keeps plan mode as a flag beside its mode, and LeapMux stores Build as the
// mode that the approval leaves plan mode for.
zcodeTest('approves a native ZCode plan and runs the native exit in the same turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const directory = createTestDirectory('zcode-approval-')
  const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, ZCODE_AGENT, { workingDir: directory, optionValues: { [OPTION_ID_PERMISSION_MODE]: 'plan' } })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const agent = await currentNativeAgent({ page, leapmuxServer })
  expect(agent.id).toBe(agentId)
  const watch = await watchNativeControls(leapmuxServer, agent.id)
  await withCleanup(async () => {
    const plan = '# Approval probe\n\n- Change no files.'
    const start = await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(AgentProvider.ZCODE, 'approval-probe', plan)] },
      { text: 'APPROVAL_RECEIVED' },
    )
    await sendMessage(page, modelScript.prompt('Request approval for a short plan titled "Approval probe".'))
    await modelScript.waitForSteps(start + 1)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    const observed = await waitForOneNativeControl(watch)
    await answerPlanReview(page, 'approve')

    await expect(banner).toHaveCount(0)
    const continuation = await modelScript.requestAt(start + 1)
    expect(JSON.stringify(continuation.body)).toContain('User has approved your plan. You can now start coding.')
    await expect(assistantBubbles(page).filter({ hasText: 'APPROVAL_RECEIVED' }).last()).toBeVisible()
    await waitForAgentIdle(page)
    await expect(savedControlAnswer(page)).toHaveText('Approve')
    await expectSettingsChip(page, 'Build')
    expect(readFileSync(join(directory, '.zcode', 'plans', `plan-${zcodePlanFileStem(agent.agentSessionId)}.md`), 'utf8')).toBe(plan)

    const { decision } = await readObservedNativeDecision({ leapmuxServer }, agent, watch, observed)
    expect(decision.request).toMatchObject({ method: ZCODE_METHOD.RequestUserInput, request: { tool_name: ZCODE_TOOL.ExitPlanMode } })
    // The app-server matches a reply by its id alone, so the reply carries no `jsonrpc`.
    expect(decision.response).toEqual({
      id: observed.payload.id,
      result: {
        [ZCODE_REPLY_FIELD.Action]: ZCODE_ACTION.Accept,
        [ZCODE_REPLY_FIELD.Content]: { [ZCODE_ANSWER_FIELD.Single]: ZCODE_PLAN_CONTROL.Approve },
      },
    })

    await page.reload()
    await expect(savedControlAnswer(page)).toHaveText('Approve')
    await expectSettingsChip(page, 'Build')
  }, async () => watch.cancel())
})

// Approving with a permission preset picks the mode the session continues in.
// ZCode cannot take that mode before its ExitPlanMode ran -- session/setMode
// turns the plan flag off, which makes the exit fail with "You are not in plan
// mode" -- so the worker hands the mode to the agent, which sends it once the
// exit's own mode event reports the flag off. The chip settles on the chosen
// mode only after ZCode itself reports running in it, and the plan still
// executes: an early setMode would have failed the exit and ended the turn
// without the approved continuation.
zcodeTest('approves a native ZCode plan into a chosen mode applied after the native exit', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const directory = createTestDirectory('zcode-approval-yolo-')
  const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, ZCODE_AGENT, { workingDir: directory, optionValues: { [OPTION_ID_PERMISSION_MODE]: 'plan' } })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const agent = await currentNativeAgent({ page, leapmuxServer })
  expect(agent.id).toBe(agentId)
  const plan = '# Bypass probe\n\n- Change no files.'
  const start = await modelScript.queue(
    { toolCalls: [exitPlanModeToolCall(AgentProvider.ZCODE, 'bypass-probe', plan)] },
    { text: 'BYPASS_APPROVAL_RECEIVED' },
  )
  await sendMessage(page, modelScript.prompt('Request approval for a short plan titled "Bypass probe".'))
  await modelScript.waitForSteps(start + 1)
  const banner = controlBanner(page)
  await expect(banner).toContainText('Plan Ready for Review')
  // The permission pill of the control actions states the mode that the approval continues in.
  await controlActions(page).getByRole('radiogroup', { name: 'Permissions' }).getByRole('radio', { name: 'Bypass' }).check()
  await answerPlanReview(page, 'approve')

  await expect(banner).toHaveCount(0)
  const continuation = await modelScript.requestAt(start + 1)
  expect(JSON.stringify(continuation.body)).toContain('User has approved your plan. You can now start coding.')
  await expect(assistantBubbles(page).filter({ hasText: 'BYPASS_APPROVAL_RECEIVED' }).last()).toBeVisible()
  await waitForAgentIdle(page)
  await expect(savedControlAnswer(page)).toHaveText('Approve')
  // The deferred Yolo mode went out after the exit: ZCode reports the session
  // in it, and the axis follows the report. A mode never sent leaves the chip
  // on the exit's own Build.
  await expectSettingsChip(page, 'Yolo')
  expect(readFileSync(join(directory, '.zcode', 'plans', `plan-${zcodePlanFileStem(agent.agentSessionId)}.md`), 'utf8')).toBe(plan)
})
