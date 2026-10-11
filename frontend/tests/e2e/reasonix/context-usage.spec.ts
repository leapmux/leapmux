import { exerciseContextUsage } from '../helpers/contextUsage'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('shows the context usage that the model reports', async ({ native }) => {
  await exerciseContextUsage(native)
})
