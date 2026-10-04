import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { agentOpenOptions, agentSettings } from '../agentSettings'
import { test } from '../fixtures'
import { openAgentViaAPI } from '../helpers/api'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { createTestDirectory } from '../helpers/runDirectory'
import { listAgents } from '../helpers/subagentRegistry'
import { openWorkspace, sendMessage } from '../helpers/ui'

test('tracks a fresh Pi implementation session after plan approval', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  const provider = AgentProvider.PI
  const agentId = await openAgentViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, authenticatedEmptyWorkspace.workspaceId, createTestDirectory('renderer-pi-fresh-plan-'), {
    agentProvider: provider,
    ...agentOpenOptions(agentSettings(provider)),
  })
  const readSession = async () => (await listAgents(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, [agentId]))?.[0]?.agentSessionId ?? ''
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
