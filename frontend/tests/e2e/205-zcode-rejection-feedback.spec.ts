import { Buffer } from 'node:buffer'
import { execFileSync } from 'node:child_process'
import { join } from 'node:path'
import { OPTION_ID_PERMISSION_MODE } from '../../src/components/chat/settingsGroups'
import { AgentProvider, ControlResponseState } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from './agentSettings'
import { expect, test } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { exitPlanModeToolCall } from './helpers/providerToolCalls'
import { createTestDirectory } from './helpers/runDirectory'
import { assistantBubbles, expectSettingsChip, openWorkspace, sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'

test('rejects a native ZCode plan and delivers approval-shaped feedback as feedback', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.setViewportSize({ width: 780, height: 1000 })
  const settings = agentOpenOptions(agentSettings(AgentProvider.ZCODE))
  const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('zcode-feedback-'), {
    agentProvider: AgentProvider.ZCODE,
    ...settings,
    optionValues: { ...settings.optionValues, [OPTION_ID_PERMISSION_MODE]: 'plan' },
  })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await expectSettingsChip(page, 'Plan')
  // Two turns: the plan request, then the answer to the rejection feedback. The
  // SUBJECT is that approval-shaped feedback text ("approve") reaches the agent
  // as FEEDBACK and not as an approval, so both turns are scripted and the
  // control round trip is the only variable left.
  await modelScript.queue({
    toolCalls: [exitPlanModeToolCall(AgentProvider.ZCODE, 'feedback-probe', '# Feedback probe\n\n- Change no files.')],
  })
  await modelScript.queue({ text: 'FEEDBACK_RECEIVED' })
  await sendMessage(page, modelScript.prompt('Request approval for a short plan titled "Feedback probe".'))
  await modelScript.waitForSteps(1)
  const banner = page.getByTestId('control-banner').filter({ visible: true })
  await expect(banner).toContainText('Plan Ready for Review')
  const editor = page.getByTestId('composer-editor').locator('.ProseMirror')
  await editor.fill('approve')
  const reject = page.getByTestId('plan-reject-btn')
  await expect(reject).toHaveText('Send feedback')
  await expect(reject).toBeInViewport({ ratio: 1 })
  await expect(page.getByTestId('queue-pause-button')).toHaveCount(0)
  await reject.click()
  await expect(banner).toHaveCount(0)
  await expect(assistantBubbles(page).filter({ hasText: 'FEEDBACK_RECEIVED' }).last()).toBeVisible()
  await waitForAgentIdle(page, 120_000)
  // The FEEDBACK, not the bare label. `feedbackOrLabel` renders the reason when
  // a deny carries one and falls back to "Rejected" only when it does not, so a
  // deny WITH feedback shows the feedback -- which is this test's whole name.
  // Asserting "Rejected" here contradicted the rule the product documents.
  //
  // The model's reasoning can move the earlier response outside the rendered
  // message window.
  const rejection = userBubbles(page).getByText('approve', { exact: true })
  if (await rejection.count() === 0)
    await page.getByRole('button', { name: 'Your response', exact: true }).first().click()
  await rejection.first().scrollIntoViewIfNeeded()
  await expect(rejection.first()).toBeInViewport({ ratio: 1 })
  // BUILD, not Plan. The chip mirrors ZCode's OWN session mode, and the agent
  // left plan mode when it called ExitPlanMode -- the control request this test
  // declines answers the APPROVAL, which the stored row below records as
  // `action: decline`. Declining does not rewind the runtime's mode, so a chip
  // still reading Plan would mean LeapMux invented a mode the session is not in.
  // This used to assert Plan and failed for that reason.
  await expectSettingsChip(page, 'Build')
  const database = join(leapmuxServer.dataDir, 'worker', 'worker.db')
  const agent = `'${agentId.replaceAll('\'', '\'\'')}'`
  const rows = JSON.parse(execFileSync('sqlite3', ['-json', database, `SELECT state, feedback, hex(resolved_content) AS content FROM control_response_answers WHERE agent_id=${agent} AND feedback='approve'`], { encoding: 'utf8' }) || '[]') as Array<{ state: number, feedback: string, content: string }>
  expect(rows).toHaveLength(1)
  const [row] = rows
  if (row === undefined)
    throw new Error('expected exactly one stored approval feedback row')
  // The column stores the proto enum ORDINAL, not its name -- see "Enum columns
  // store proto enum ordinals" in CLAUDE.md. Binding the generated constant
  // means a renumber propagates here instead of leaving a stale literal.
  expect(row.state).toBe(ControlResponseState.COMPLETED)
  expect(JSON.parse(Buffer.from(row.content, 'hex').toString()).result.action).toBe('decline')
  expect(errors).toEqual([])
})
