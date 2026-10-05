import { expect } from '@playwright/test'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest } from '../codewhale-fixtures'
import { compactionNoticeRow, expectCompactionNotice } from '../helpers/compaction'
import { exerciseManualCompaction } from '../helpers/manualCompaction'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

/**
 * Codewhale 0.10.0 ends a compaction with `item.completed` for a `context_compaction`
 * item. The item states "Compaction complete: ..." as its summary, and the event states
 * `auto: false` for a manual compaction. The transcript draws that item as the completed
 * notice, with the trigger that the event states.
 */
codewhaleTest('shows a completed native compaction notice and retains it after reload', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
  void authenticatedCodewhaleWorkspace
  await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'You are performing a context checkpoint compaction' })
  await expectCompactionNotice(page)
  await expect(compactionNoticeRow(page)).toContainText('manual')
  await page.reload()
  await expectCompactionNotice(page)
  await expect(compactionNoticeRow(page)).toContainText('manual')
})
