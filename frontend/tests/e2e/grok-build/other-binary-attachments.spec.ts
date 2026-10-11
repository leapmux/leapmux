import { grokTest } from '../grok-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { expectGrokCopiedBytes } from './attachmentScenarios'

grokTest.describe('Grok Build attachments', () => {
  grokTest('accepts a binary attachment and carries it through the turn', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'binary', 'grok-blob.bin', {
      proof: (request, sourcePath) => expectGrokCopiedBytes(request.body, sourcePath, 'application/macbinary'),
    })
  })
})
