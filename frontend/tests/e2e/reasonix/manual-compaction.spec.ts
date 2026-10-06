import { reasonixTest } from '../reasonix-fixtures'
import { proveNoNativeManualCompaction } from './compactionScenario'

reasonixTest('passes the slash command to the model in ACP mode', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
  void authenticatedReasonixWorkspace
  await proveNoNativeManualCompaction(page, modelScript)
})
