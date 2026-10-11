import { exerciseContextUsage } from '../helpers/contextUsage'
import { qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code Basic Chat', () => {
  qwenTest('reports model usage in the agent info card', async ({ native }) => {
    await exerciseContextUsage(native)
  })
})
