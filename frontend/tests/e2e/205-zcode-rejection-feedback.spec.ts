import { Buffer } from 'node:buffer'
import { execFileSync } from 'node:child_process'
import { join } from 'node:path'
import { OPTION_ID_PERMISSION_MODE } from '../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { createTestDirectory } from './helpers/runDirectory'
import { assistantBubbles, expectSettingsChip, openWorkspace, sendMessage, userBubbles, waitForAgentIdle } from './helpers/ui'
import { realAgentOpenOptions, realAgentSettings } from './realAgentSettings'

test('rejects a native ZCode plan and delivers approval-shaped feedback as feedback', async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  await page.setViewportSize({ width: 780, height: 1000 })
  const settings = realAgentOpenOptions(realAgentSettings(AgentProvider.ZCODE))
  const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('zcode-feedback-'), {
    agentProvider: AgentProvider.ZCODE,
    ...settings,
    optionValues: { ...settings.optionValues, [OPTION_ID_PERMISSION_MODE]: 'plan' },
  })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await expectSettingsChip(page, 'Plan')
  await sendMessage(page, 'This is a control protocol test. Use ExitPlanMode to request approval for a short plan titled "Feedback probe". The plan does not change any files. Do not implement it. After a rejection, end your turn without other tools. If a later user message supplies feedback on the rejected request, reply exactly FEEDBACK_RECEIVED and do not use tools.')
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
  // The model's reasoning can move the earlier response outside the rendered message window.
  const rejection = userBubbles(page).getByText('Rejected', { exact: true })
  if (await rejection.count() === 0)
    await page.getByRole('button', { name: 'Your response', exact: true }).first().click()
  await rejection.scrollIntoViewIfNeeded()
  await expect(rejection).toBeInViewport({ ratio: 1 })
  await expectSettingsChip(page, 'Plan')
  const database = join(leapmuxServer.dataDir, 'worker', 'worker.db')
  const agent = `'${agentId.replaceAll('\'', '\'\'')}'`
  const rows = JSON.parse(execFileSync('sqlite3', ['-json', database, `SELECT state, feedback, hex(resolved_content) AS content FROM control_response_answers WHERE agent_id=${agent} AND feedback='approve'`], { encoding: 'utf8' }) || '[]') as Array<{ state: string, feedback: string, content: string }>
  expect(rows).toHaveLength(1)
  expect(rows[0].state).toBe('completed')
  expect(JSON.parse(Buffer.from(rows[0].content, 'hex').toString()).result.action).toBe('decline')
  expect(errors).toEqual([])
})
