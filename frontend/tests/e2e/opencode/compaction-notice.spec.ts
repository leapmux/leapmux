import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { opencodeTest } from '../opencode-fixtures'
import { OPENCODE_COMPACTION } from './compactionScenario'

opencodeTest('completes actual native compaction without a completed notice row', async ({ native }) => {
  await expectNoCompactionNotice(native, { relatedProof: () => exerciseNativeCompaction(native, OPENCODE_COMPACTION) })
})
