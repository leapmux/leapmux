import { codexTest } from '../codex-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'

codexTest.describe('Codex context usage', () => {
  codexTest('the agent info grid follows the usage the model reports', async ({ native }) => {
    await exerciseContextUsage(native)
  })
})
