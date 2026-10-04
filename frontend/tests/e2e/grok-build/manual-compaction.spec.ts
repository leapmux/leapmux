import { GROK_E2E_SKIP_REASON, grokTest } from '../grok-fixtures'
import { exerciseManualCompaction } from '../helpers/manualCompaction'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest.describe('Grok Build Basic Chat', () => {
  grokTest('compacts a scripted conversation on request', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'Your task is to produce a faithful, concise summary of the conversation so far' })
  })
})
