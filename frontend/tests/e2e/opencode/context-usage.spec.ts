import { exerciseContextUsage } from '../helpers/contextUsage'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('shows the context usage that the model reports', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
  void authenticatedOpencodeWorkspace
  await exerciseContextUsage(page, modelScript)
})
