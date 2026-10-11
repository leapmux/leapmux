import { grokTest } from '../grok-fixtures'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { GROK_COMPACTION } from './compactionScenario'

grokTest('proves native context compaction without a completed compaction notice', async ({ native }) => {
  await expectNoCompactionNotice(native, { relatedProof: () => exerciseNativeCompaction(native, GROK_COMPACTION) })
})
