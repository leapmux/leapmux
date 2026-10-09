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
import { museCompactionSummaryStep, museSummarizerMatcher } from './compactionScenario.muse'

museTest('draws and keeps the native notice after a manual compaction', async ({ native }) => {
  // Muse counts occupancy from the provider-reported facts, so the seeds state
  // real input counts: the context crosses the soft threshold (the manual
  // compaction runs) and stays under the hard one (nothing compacts on its own).
  await exerciseNativeCompaction(native, {
    summary: { route: 'rule', when: museSummarizerMatcher, respond: museCompactionSummaryStep() },
    reportedInputTokens: 20000,
  })
  await expectCompactionNoticeAfterReload(native.page)
})
