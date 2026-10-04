import { exerciseContextUsage } from '../helpers/contextUsage'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest.describe('Qwen Code Basic Chat', () => {
  qwenTest('reports model usage in the agent info card', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    await exerciseContextUsage(page, modelScript)
  })
})
