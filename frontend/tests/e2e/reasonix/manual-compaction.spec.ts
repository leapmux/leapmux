import { REASONIX_E2E_SKIP_REASON, reasonixTest } from '../reasonix-fixtures'
import { proveNoNativeManualCompaction } from './compactionScenario'

reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')

reasonixTest('passes the slash command to the model in ACP mode', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
  void authenticatedReasonixWorkspace
  await proveNoNativeManualCompaction(page, modelScript)
})
