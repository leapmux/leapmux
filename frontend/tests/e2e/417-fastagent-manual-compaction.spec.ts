import { expect, FAST_AGENT_E2E_SKIP_REASON, fastAgentTest } from './fastagent-fixtures'
import { compactionNoticeRow } from './helpers/compaction'
import { messageBubbles, sendMessage, waitForAgentIdle } from './helpers/ui'

fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

fastAgentTest('refuses the compact command on its ACP path', async ({ authenticatedFastAgentWorkspace, page }) => {
  void authenticatedFastAgentWorkspace
  await sendMessage(page, '/compact preview')
  await waitForAgentIdle(page, 120_000)
  await expect(messageBubbles(page).filter({ hasText: 'Unknown command: /compact' }).first()).toBeVisible()
})

fastAgentTest('refuses the exact compact command without a completed boundary', async ({ authenticatedFastAgentWorkspace, page }) => {
  void authenticatedFastAgentWorkspace
  await sendMessage(page, '/compact')
  await waitForAgentIdle(page, 120_000)
  await expect(messageBubbles(page).filter({ hasText: 'Unknown command: /compact' }).first()).toBeVisible()
  await expect(compactionNoticeRow(page)).toHaveCount(0)
})
