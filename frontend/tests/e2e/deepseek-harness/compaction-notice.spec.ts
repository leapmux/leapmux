import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { compactionIndicator, expectCompactionNoticeAfterReload, expectNoLiveCompaction } from '../helpers/compaction'
import { exerciseDeepseekHarnessCompaction } from './compactionScenarios'

deepseekHarnessTest('shows the completed native compaction boundary and keeps it after reload', async ({ native }) => {
  await exerciseDeepseekHarnessCompaction(native, {
    summaryGate: 'deepseek-harness-manual-summary',
    onHeld: async () => { await expect(compactionIndicator(native.page)).toBeVisible() },
  })
  await expectCompactionNoticeAfterReload(native.page, { count: 1, divider: true })
  await expectNoLiveCompaction(native.page)
})
