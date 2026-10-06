import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { qwenTest } from '../qwen-fixtures'
import { QWEN_COMPACTION } from './compactionScenario'

qwenTest('proves native context compaction without a completed compaction notice', async ({ native }) => {
  await expectNoCompactionNotice(native, { relatedProof: () => exerciseNativeCompaction(native, QWEN_COMPACTION) })
})
