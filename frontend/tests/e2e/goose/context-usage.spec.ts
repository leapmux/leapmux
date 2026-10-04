import { GOOSE_E2E_SKIP_REASON, gooseTest } from '../goose-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'

gooseTest.skip(!!GOOSE_E2E_SKIP_REASON, GOOSE_E2E_SKIP_REASON || '')

gooseTest('shows the context usage that the model reports', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
  void authenticatedGooseWorkspace
  await exerciseContextUsage(page, modelScript)
})
