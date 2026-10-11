import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { qwenTest } from '../qwen-fixtures'
import { QWEN_COMPACTION } from './compactionScenario'

qwenTest.describe('Qwen Code Basic Chat', () => {
  qwenTest('compacts a scripted conversation on request', async ({ native }) => {
    await exerciseNativeCompaction(native, QWEN_COMPACTION)
  })
})
