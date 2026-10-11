import { exerciseContextUsage } from '../helpers/contextUsage'
import { piTest } from '../pi-fixtures'

piTest('shows the context usage that the model reports', async ({ native }) => {
  await exerciseContextUsage(native)
})
