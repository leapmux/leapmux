import { droidTest } from '../droid-fixtures'
import { exerciseCompletedManualCompaction } from './compactionScenarios'

droidTest.describe('Factory Droid compaction', () => {
  droidTest('shows and keeps the native manual compaction notice', async ({ native }) => {
    await exerciseCompletedManualCompaction(native)
  })
})
