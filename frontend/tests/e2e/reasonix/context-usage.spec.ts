import { exerciseContextUsage } from '../helpers/contextUsage'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from '../reasonix-fixtures'

reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')

reasonixTest('shows the context usage that the model reports', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
  void authenticatedReasonixWorkspace
  await exerciseContextUsage(page, modelScript)
})
