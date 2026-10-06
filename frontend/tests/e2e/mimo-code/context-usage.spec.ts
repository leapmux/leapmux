import { exerciseContextUsage } from '../helpers/contextUsage'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code basic chat', () => {
  mimoTest('reports model usage in the agent info card', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await exerciseContextUsage(page, modelScript)
  })
})
