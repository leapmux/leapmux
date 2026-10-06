import { exerciseContextUsage } from '../helpers/contextUsage'
import { qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code Basic Chat', () => {
  qwenTest('reports model usage in the agent info card', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    await exerciseContextUsage(page, modelScript)
  })
})
