import { exerciseManualCompaction } from '../helpers/manualCompaction'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('compacts a scripted conversation on request', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
  void authenticatedOpencodeWorkspace
  await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'Create a new anchored summary from the conversation history' })
})
