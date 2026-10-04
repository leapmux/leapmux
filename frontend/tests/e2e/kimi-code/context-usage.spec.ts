import { exerciseContextUsage } from '../helpers/contextUsage'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

kimiTest.describe('uses Kimi Code for basic chat', () => {
  kimiTest('reports model usage in the agent info card', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await exerciseContextUsage(page, modelScript)
  })
})
