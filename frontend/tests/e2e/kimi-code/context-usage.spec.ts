import { exerciseContextUsage } from '../helpers/contextUsage'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('uses Kimi Code for basic chat', () => {
  kimiTest('reports model usage in the agent info card', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await exerciseContextUsage(page, modelScript)
  })
})
