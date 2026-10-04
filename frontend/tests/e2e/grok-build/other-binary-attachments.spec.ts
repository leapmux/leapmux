import { GROK_E2E_SKIP_REASON, grokTest } from '../grok-fixtures'

import { exerciseGrokAttachment, expectGrokCopiedBytes } from './attachmentScenarios'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest.describe('Grok Build attachments', () => {
  grokTest('accepts a binary attachment and carries it through the turn', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await exerciseGrokAttachment(page, modelScript, 'binary', 'grok-blob.bin', (body, sourcePath) => {
      expectGrokCopiedBytes(body, sourcePath, 'application/macbinary')
    })
  })
})
