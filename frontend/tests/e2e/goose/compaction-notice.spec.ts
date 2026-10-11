import { gooseTest } from '../goose-fixtures'
import { exerciseGooseCompaction } from './compactionScenario'

gooseTest('keeps the completed native compaction status after reload', async ({ native }) => {
  await exerciseGooseCompaction(native)
})
