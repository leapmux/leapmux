import { expectCompactionNotice } from '../helpers/compaction'
import { exerciseManualCompaction } from '../helpers/manualCompaction'
import { waitForAgentIdle } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

// MiMo Code 0.1.15 sends its compaction part twice: once when the compaction
// starts, and once when it ends, with the summary in the part's projection. The
// worker persists both as notifications, and the transcript folds the start
// into the end, which it draws as the completed notice.
mimoTest('proves native context compaction and keeps the completed compaction notice after reload', async ({ page, modelScript, authenticatedMiMoWorkspace }) => {
  void authenticatedMiMoWorkspace
  await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'Write a continuation summary that will allow you' })
  await expectCompactionNotice(page)
  await page.reload()
  await waitForAgentIdle(page)
  await expectCompactionNotice(page)
})
