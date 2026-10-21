import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { compactionIndicator, expectCompactionNoticeAfterReload, expectNoLiveCompaction } from '../helpers/compaction'
import { exerciseCompletedManualCompaction } from './compactionScenarios'

droidTest('keeps the completed native compaction status after reload', async ({ native }) => {
  await exerciseCompletedManualCompaction(native, {
    summaryGate: 'factory-droid-manual-summary',
    onHeld: async () => { await expect(compactionIndicator(native.page)).toBeVisible() },
  })
  await expectCompactionNoticeAfterReload(native.page, { count: 1, divider: true })
  await expectNoLiveCompaction(native.page)
})
