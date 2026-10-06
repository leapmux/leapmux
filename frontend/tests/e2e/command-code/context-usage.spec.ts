import { commandCodeTest } from '../command-code-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'

commandCodeTest('shows the exact native context usage and keeps explicit zero values', async ({ authenticatedCommandCodeWorkspace, page, modelScript }) => {
  void authenticatedCommandCodeWorkspace
  await exerciseContextUsage(page, modelScript)
})
