import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { expectNoCompactionNotice } from '../helpers/unsupportedCompaction'
import { kiloTest } from '../kilo-fixtures'
import { OPENCODE_COMPACTION } from '../opencode/compactionScenario'

kiloTest('completes actual native compaction without a completed notice row', async ({ native }) => {
  await expectNoCompactionNotice(native, { relatedProof: () => exerciseNativeCompaction(native, OPENCODE_COMPACTION) })
})
