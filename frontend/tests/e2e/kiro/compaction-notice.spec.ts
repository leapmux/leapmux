import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { kiroTest } from '../kiro-fixtures'
import { KIRO_COMPACTION } from './compactionScenario'

kiroTest('proves native context compaction without a completed compaction notice', async ({ native }) => {
  await expectNoCompactionNotice(native, { relatedProof: () => exerciseNativeCompaction(native, KIRO_COMPACTION) })
})
