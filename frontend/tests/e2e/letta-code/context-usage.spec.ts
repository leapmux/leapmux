import { exerciseContextUsage } from '../helpers/contextUsage'
import { lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code attachments and context usage', () => {
  lettaTest('the agent info grid follows the usage the model reports', async ({ native }) => {
    await exerciseContextUsage(native)
  })
})
