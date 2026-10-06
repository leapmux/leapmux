import { grokTest } from '../grok-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { expectGrokCopiedBytes } from './attachmentScenarios'

grokTest.describe('Grok Build attachments', () => {
  grokTest('accepts a PDF attachment and carries it through the turn', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'pdf', 'grok-doc.pdf', {
      proof: (request, sourcePath) => expectGrokCopiedBytes(request.body, sourcePath, 'application/pdf'),
    })
  })
})
