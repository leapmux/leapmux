import { commandCodeTest } from '../command-code-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'

commandCodeTest('shows the exact native context usage and keeps explicit zero values', async ({ commandCodeWorkspace, page, modelScript }) => {
  void commandCodeWorkspace
  await exerciseContextUsage(page, modelScript)
})
