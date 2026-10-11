import { claudeTest } from '../claude-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'

claudeTest('shows native context usage and restores it after reload', async ({ native }) => {
  await exerciseContextUsage(native, { reload: true })
})
