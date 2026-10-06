import { exerciseContextUsage } from '../helpers/contextUsage'
import { piTest } from '../pi-fixtures'

piTest('shows the context usage that the model reports', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  void authenticatedPiWorkspace
  await exerciseContextUsage(page, modelScript)
})
