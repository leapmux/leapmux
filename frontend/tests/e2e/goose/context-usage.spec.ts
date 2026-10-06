import { gooseTest } from '../goose-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'

gooseTest('shows the context usage that the model reports', async ({ native }) => {
  await exerciseContextUsage(native)
})
