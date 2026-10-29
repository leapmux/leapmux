/**
 * A completed native Muse compaction draws a notice row that survives a reload.
 *
 * The private settings declare a config model catalog with context limits, so
 * the host's compaction thresholds resolve: /compact starts the native
 * summarizer, whose generate_summary answer becomes the compacted context.
 */
import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { museTest } from '../muse-fixtures'
import { MUSE_COMPACTION_REPORTED_INPUT_TOKENS, museCompactionSummaryStep, museSummarizerMatcher } from './compactionScenario.muse'

museTest('draws and keeps the native notice after a manual compaction', async ({ native }) => {
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
})
