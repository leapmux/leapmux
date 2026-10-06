import { exerciseContextUsage } from '../helpers/contextUsage'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('shows the context usage that the model reports', async ({ native }) => {
  await exerciseContextUsage(native)
})
