import { GROK_E2E_SKIP_REASON, grokTest } from '../grok-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest.describe('Grok Build Basic Chat', () => {
  grokTest('reports model usage in the agent info card', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await exerciseContextUsage(page, modelScript)
  })
})
