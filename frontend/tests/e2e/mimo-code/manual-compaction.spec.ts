import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { mimoTest } from '../mimo-fixtures'
import { MIMO_COMPACTION } from './compactionScenario'

mimoTest.describe('MiMo Code basic chat', () => {
  mimoTest('compacts a scripted conversation on request', async ({ native }) => {
    await exerciseNativeCompaction(native, MIMO_COMPACTION)
  })
})
