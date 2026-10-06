import { gooseTest } from '../goose-fixtures'
import { exerciseGooseCompaction } from './compactionScenario'

gooseTest('runs the native slash command and removes old context', async ({ native }) => {
  await exerciseGooseCompaction(native)
})
