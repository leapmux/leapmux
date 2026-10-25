import { Buffer } from 'node:buffer'
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { ZCODE_ACTION, ZCODE_ANSWER_FIELD, ZCODE_METHOD, ZCODE_PLAN_CONTROL, ZCODE_REPLY_FIELD, ZCODE_TOOL } from '../../../src/generated/contracts/zcode-protocol'
import { AgentInputKind, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ControlDeliveryState } from '../../../src/generated/proto/leapmux/v1/hub_storage_pb'
import { RAIL } from '../helpers/chatScrollRail'
import { withCleanup } from '../helpers/cleanup'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readObservedNativeDecision, waitForOneNativeControl } from '../helpers/nativeStoredControlDecision'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { newProviderWorkingDir } from '../helpers/providerWorkingDir'
import { answerPlanReview, assistantBubbles, controlActions, controlBanner, enterControlFeedback, expectSettingsChip, messageContents, openWorkspace, planReviewButton, savedControlAnswer, sendMessage, userBubbles, visibleOnly, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { zcodeTest } from '../zcode-fixtures'
import { ZCODE_AGENT } from './scenarios'

zcodeTest('rejects a native ZCode plan and delivers approval-shaped feedback as feedback', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.setViewportSize({ width: 780, height: 1000 })
  const { agentId } = await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, ZCODE_AGENT, { directoryPrefix: 'zcode-feedback-', optionValues: { [OPTION_ID_PERMISSION_MODE]: 'plan' } })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await expectSettingsChip(page, 'Plan')
  // Script the plan request and the reply to its feedback as separate turns.
  // Approval-shaped feedback must remain feedback.
  const start = await modelScript.queue(
    { toolCalls: [exitPlanModeToolCall(AgentProvider.ZCODE, 'feedback-probe', '# Feedback probe\n\n- Change no files.')] },
    { text: 'FEEDBACK_RECEIVED' },
  )
  await sendMessage(page, modelScript.prompt('Request approval for a short plan titled "Feedback probe".'))
  await modelScript.waitForSteps(start + 1)
  const banner = controlBanner(page)
  await expect(banner).toContainText('Plan Ready for Review')
  await enterControlFeedback(page, 'approve')
  const reject = planReviewButton(page, 'reject')
  await expect(reject).toHaveText('Send feedback')
  await expect(reject).toBeInViewport({ ratio: 1 })
  // No queue toggle, visible or hidden, while the composer holds the feedback of a control.
  await expect(page.getByTestId('queue-pause-button')).toHaveCount(0)
  await answerPlanReview(page, 'reject')
  await expect(banner).toHaveCount(0)
  await expect(assistantBubbles(page).filter({ hasText: 'FEEDBACK_RECEIVED' }).last()).toBeVisible()
  await waitForAgentIdle(page)
  // The saved rejection shows the feedback text. The scroll rail can restore its row after virtualization.
  const rejection = userBubbles(page).getByText('approve', { exact: true })
  if (await rejection.count() === 0)
    await visibleOnly(page.locator(RAIL).getByRole('button', { name: 'Your response', exact: true })).click()
  await rejection.scrollIntoViewIfNeeded()
  await expect(rejection).toBeInViewport({ ratio: 1 })
  // A rejection leaves planEnabled true. The native mode stays build, so the chip must follow the plan flag.
  await expectSettingsChip(page, 'Plan')
  const database = join(leapmuxServer.dataDir, 'worker', 'worker.db')
  const agent = `'${agentId.replaceAll('\'', '\'\'')}'`
  // The native decline cannot carry the feedback, so the answer queues it as a follow-up input of its own.
  const rows = JSON.parse(execFileSync('sqlite3', ['-json', database, `SELECT state, follow_up_kind, hex(resolved_content) AS content FROM control_response_answers WHERE agent_id=${agent} AND follow_up='approve'`], { encoding: 'utf8' }) || '[]') as Array<{ state: number, follow_up_kind: number, content: string }>
  expect(rows).toHaveLength(1)
  const [row] = rows
  if (row === undefined)
    throw new Error('expected exactly one stored approval feedback row')
  // This view exposes the delivery enum stored in control_deliveries, not the public response enum.
  expect(row.state).toBe(ControlDeliveryState.COMPLETED)
  expect(row.follow_up_kind).toBe(AgentInputKind.CONTROL_FEEDBACK)
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

// An approved ExitPlanMode writes the plan file and leaves plan mode.
// Its native result makes the model continue in the same turn.
zcodeTest('approves a native ZCode plan and runs the native exit in the same turn', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const directory = newProviderWorkingDir(ZCODE_AGENT, 'zcode-approval-')
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

// ZCode applies the selected preset after ExitPlanMode reports that plan mode ended.
// An earlier mode change would make ExitPlanMode fail and prevent the native continuation.
zcodeTest('approves a native ZCode plan into a chosen mode applied after the native exit', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const directory = newProviderWorkingDir(ZCODE_AGENT, 'zcode-approval-yolo-')
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
  // The native report confirms that the deferred preset replaced Build after the plan exit.
  await expectSettingsChip(page, 'Yolo')
  expect(readFileSync(join(directory, '.zcode', 'plans', `plan-${zcodePlanFileStem(agent.agentSessionId)}.md`), 'utf8')).toBe(plan)
})

zcodeTest('approves a native ZCode plan with Clear Context and leaves no card of the retired session', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, ZCODE_AGENT, {
    directoryPrefix: 'zcode-clear-plan-',
    optionValues: { [OPTION_ID_PERMISSION_MODE]: 'plan' },
  })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const start = await modelScript.queue(
    { toolCalls: [exitPlanModeToolCall(AgentProvider.ZCODE, 'clear-plan', modelScript.prompt('# Clear context plan\n\n- Change no files.'))] },
    { text: 'PLAN_RAN' },
  )
  await sendMessage(page, modelScript.prompt('Request approval for the scripted plan.'))
  await modelScript.waitForSteps(start + 1)
  await expect(controlBanner(page)).toContainText('Plan Ready for Review')
  await visibleOnly(page.getByTestId('plan-clear-context-checkbox')).locator('input[type="checkbox"]').check()
  await answerPlanReview(page, 'approve')
  await modelScript.waitForSteps(start + 2)
  await expect(messageContents(page).filter({ hasText: 'Context cleared' })).toHaveCount(1)
  await expect(assistantBubbles(page).filter({ hasText: 'PLAN_RAN' })).toHaveCount(1)
  await waitForAgentIdle(page)
  await expect(controlBanner(page)).toHaveCount(0)
  const continuation = await modelScript.requestAt(start + 1)
  expect(JSON.stringify(continuation.body)).toContain('Clear context plan')
  expect(JSON.stringify(continuation.body)).not.toContain('User has approved your plan. You can now start coding.')
})

zcodeTest('stops a ZCode turn that waits for plan approval and marks it interrupted', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, ZCODE_AGENT, {
    directoryPrefix: 'zcode-stop-plan-',
    optionValues: { [OPTION_ID_PERMISSION_MODE]: 'plan' },
  })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  const start = await modelScript.queue({ toolCalls: [exitPlanModeToolCall(AgentProvider.ZCODE, 'stop-plan', '# Stop this plan\n\n- Change no files.')] })
  await sendMessage(page, modelScript.prompt('Request approval for the scripted plan.'))
  await modelScript.waitForSteps(start + 1)
  await expect(controlBanner(page)).toContainText('Plan Ready for Review')
  await controlBanner(page).getByTestId('control-interrupt').click()
  await waitForAgentIdle(page)
  await expect(controlBanner(page)).toHaveCount(0)
  await expect(messageContents(page).filter({ hasText: 'Turn interrupted' })).toHaveCount(1)
})
