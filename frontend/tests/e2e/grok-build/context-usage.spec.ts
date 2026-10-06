import { grokTest } from '../grok-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'

grokTest.describe('Grok Build Basic Chat', () => {
  grokTest('reports model usage in the agent info card', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await exerciseContextUsage(page, modelScript)
  })
})
