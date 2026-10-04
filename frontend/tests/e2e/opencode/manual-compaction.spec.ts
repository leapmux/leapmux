import { exerciseManualCompaction } from '../helpers/manualCompaction'
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from '../opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest('compacts a scripted conversation on request', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
  void authenticatedOpencodeWorkspace
  await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'Create a new anchored summary from the conversation history' })
})
