import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { expectCompactionNotice } from '../helpers/compaction'
import { seedManualCompactionConversation } from '../helpers/manualCompaction'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'

codexTest('shows a completed native compaction notice and retains it after reload', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
  void authenticatedCodexWorkspace
  await modelScript.rule({ name: 'notice-compaction-summary', when: { user: ['compact', 'summary'] }, respond: { text: 'The earlier work is summarized.' } })
  await seedManualCompactionConversation(page, modelScript)
  await sendMessage(page, '/compact')
  await expect.poll(async () => (await modelScript.status()).ruleMatches['notice-compaction-summary'] ?? 0).toBeGreaterThan(0)
  await waitForAgentIdle(page)
  await expectCompactionNotice(page)
  await page.reload()
  await expectCompactionNotice(page)
})
