import { exerciseContextUsage } from '../helpers/contextUsage'
import { kiloTest } from '../kilo-fixtures'

kiloTest('shows the context usage that the model reports', async ({ native }) => {
  await exerciseContextUsage(native)
})
