/**
 * A user /compact command starts Muse's native context compaction, and the next
 * model turn uses the compacted context in place of the older history.
 */
import { expectCompactionNoticeAfterReload, expectNoLiveCompaction } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { museTest } from '../muse-fixtures'
import { MUSE_COMPACTION_REPORTED_INPUT_TOKENS, museCompactionSummaryStep, museSummarizerMatcher } from './compactionScenario.muse'

museTest('manual-compaction: replaces the older context with the native summary', async ({ native }) => {
  // The seed responses report usage above the soft threshold and below the
  // hard threshold. Manual compaction runs without an earlier automatic summary.
  await exerciseNativeCompaction(native, {
    summary: {
      route: 'rule',
      when: museSummarizerMatcher,
      respond: museCompactionSummaryStep(),
    },
    reportedInputTokens: MUSE_COMPACTION_REPORTED_INPUT_TOKENS,
  })
  await expectCompactionNoticeAfterReload(native.page)
  await expectNoLiveCompaction(native.page)
})
