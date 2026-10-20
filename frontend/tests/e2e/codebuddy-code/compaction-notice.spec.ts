import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { compactionIndicator, expectCompactionNoticeAfterReload, expectNoLiveCompaction } from '../helpers/compaction'
import { exerciseContextCompaction } from './compactionScenarios'

// CodeBuddy frames no compaction of its own: the worker publishes the live
// compaction while the command runs and stores one boundary row at its end
// (decision 21). The held summary proves the live indicator before that end.
codebuddyTest('ends the actual native compaction with the worker boundary row', async ({ native }) => {
  await exerciseContextCompaction(native, {
    summaryGate: 'codebuddy-manual-summary',
    onHeld: async () => {
      await expect(compactionIndicator(native.page)).toBeVisible()
    },
  })
  await expectCompactionNoticeAfterReload(native.page, { divider: true })
  await expectNoLiveCompaction(native.page)
})
