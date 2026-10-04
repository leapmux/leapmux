import { exerciseContextUsage } from '../helpers/contextUsage'
import { PI_E2E_SKIP_REASON, piTest } from '../pi-fixtures'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

piTest('shows the context usage that the model reports', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  void authenticatedPiWorkspace
  await exerciseContextUsage(page, modelScript)
})
