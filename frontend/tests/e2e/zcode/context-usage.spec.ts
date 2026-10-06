import { exerciseContextUsage } from '../helpers/contextUsage'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('shows the context usage that the model reports', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
  void authenticatedZCodeWorkspace
  await exerciseContextUsage(page, modelScript)
})
