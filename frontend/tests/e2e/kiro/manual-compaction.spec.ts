import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { kiroTest } from '../kiro-fixtures'
import { KIRO_COMPACTION } from './compactionScenario'

kiroTest.describe('Kiro basic chat', () => {
  kiroTest('compacts a scripted conversation on request', async ({ native }) => {
    await exerciseNativeCompaction(native, KIRO_COMPACTION)
  })
})
