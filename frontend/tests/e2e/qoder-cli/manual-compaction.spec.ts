import { qoderTest } from '../qoder-fixtures'
import { exerciseCompletedManualCompaction, exerciseFailedManualCompaction } from './compactionScenarios'

qoderTest.describe('Qoder CLI compaction and rate limits', () => {
  qoderTest('uses a native manual summary and shows its completed boundary', async ({ native }) => {
    await exerciseCompletedManualCompaction(native)
  })

  qoderTest('does not claim a failed native manual compaction succeeded', async ({ native }) => {
    await exerciseFailedManualCompaction(native)
  })
})
