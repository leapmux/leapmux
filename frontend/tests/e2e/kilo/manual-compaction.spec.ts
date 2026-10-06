import { exerciseManualCompaction } from '../helpers/manualCompaction'
import { kiloTest } from '../kilo-fixtures'

kiloTest('compacts a scripted conversation on request', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
  void authenticatedKiloWorkspace
  await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'Create a new anchored summary from the conversation history' })
})
