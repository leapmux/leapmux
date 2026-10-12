import { exerciseContextUsage } from '../helpers/contextUsage'
import { junieTest } from '../junie-fixtures'

junieTest.describe('Junie attachments and context usage', () => {
  junieTest('the agent info grid follows the usage the model reports', async ({ native }) => {
    await exerciseContextUsage(native)
  })
})
