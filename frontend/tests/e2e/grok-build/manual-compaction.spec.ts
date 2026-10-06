import { grokTest } from '../grok-fixtures'
import { exerciseManualCompaction } from '../helpers/manualCompaction'

grokTest.describe('Grok Build Basic Chat', () => {
  grokTest('compacts a scripted conversation on request', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'Your task is to produce a faithful, concise summary of the conversation so far' })
  })
})
