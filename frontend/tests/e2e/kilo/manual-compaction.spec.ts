import { exerciseManualCompaction } from '../helpers/manualCompaction'
import { KILO_E2E_SKIP_REASON, kiloTest } from '../kilo-fixtures'

kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')

kiloTest('compacts a scripted conversation on request', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
  void authenticatedKiloWorkspace
  await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'Create a new anchored summary from the conversation history' })
})
