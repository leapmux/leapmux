import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { kiloTest } from '../kilo-fixtures'
import { OPENCODE_COMPACTION } from '../opencode/compactionScenario'

kiloTest('compacts a scripted conversation on request', async ({ native }) => {
  await exerciseNativeCompaction(native, OPENCODE_COMPACTION)
})
