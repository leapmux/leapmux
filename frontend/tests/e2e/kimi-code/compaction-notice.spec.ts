import { expect } from '@playwright/test'
import { compactionNoticeRow } from '../helpers/compaction'
import { exerciseManualCompaction } from '../helpers/manualCompaction'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

kimiTest('shows the actual native completed compaction notice and preserves it after reload', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
  void authenticatedKimiWorkspace
  await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'You are about to run out of context' })
  const notice = compactionNoticeRow(page)
  await expect(notice).toBeVisible()
  await expect(notice).toContainText('Context compacted')
  await page.reload()
  await expect(notice).toBeVisible()
})
