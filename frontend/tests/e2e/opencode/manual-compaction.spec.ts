import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { opencodeTest } from '../opencode-fixtures'
import { OPENCODE_COMPACTION } from './compactionScenario'

opencodeTest('compacts a scripted conversation on request', async ({ native }) => {
  await exerciseNativeCompaction(native, OPENCODE_COMPACTION)
})
