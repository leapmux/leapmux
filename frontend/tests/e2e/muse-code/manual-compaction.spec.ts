/**
 * A user /compact command starts Muse's native context compaction, and the next
 * model turn uses the compacted context in place of the older history.
 */
import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { museTest } from '../muse-fixtures'
import { museCompactionSummaryStep, museSummarizerMatcher } from './compactionScenario.muse'

museTest('manual-compaction: replaces the older context with the native summary', async ({ native }) => {
  // Muse counts occupancy from the provider-reported facts, so the seeds state
  // real input counts: the context crosses the soft threshold (the manual
  // compaction runs) and stays under the hard one (nothing compacts on its own).
  await exerciseNativeCompaction(native, {
    summary: { route: 'rule', when: museSummarizerMatcher, respond: museCompactionSummaryStep() },
    reportedInputTokens: 20000,
  })
  await expectCompactionNoticeAfterReload(native.page)
})
