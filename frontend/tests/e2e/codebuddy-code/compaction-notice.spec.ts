import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { compactionIndicator, expectNoLiveCompaction } from '../helpers/compaction'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { exerciseContextCompactionWithoutNotice } from './compactionScenarios'

codebuddyTest('receives no completed compaction notice from the actual native command', async ({ native }) => {
  await expectNoCompactionNotice(native, { relatedProof: () => exerciseContextCompactionWithoutNotice(native, {
    summaryGate: 'codebuddy-manual-summary',
    onHeld: async () => { await expect(compactionIndicator(native.page)).toBeVisible() },
  }) })
  await expectNoLiveCompaction(native.page)
})
