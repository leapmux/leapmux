import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'

codebuddyTest.describe('CodeBuddy Code attachments and context usage', () => {
  codebuddyTest('the agent info grid follows the usage the model reports', async ({ native }) => {
    await exerciseContextUsage(native)
  })
})
