import { exerciseContextUsage } from '../helpers/contextUsage'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code basic chat', () => {
  mimoTest('reports model usage in the agent info card', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await exerciseContextUsage(page, modelScript)
  })
})
