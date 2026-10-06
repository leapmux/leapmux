import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { kiroTest } from '../kiro-fixtures'

kiroTest.describe('Kiro attachments', () => {
  kiroTest('accepts a PDF attachment and carries it through the turn', async ({ native }) => {
    // Kiro sends the original bytes to its service as a `documents` entry with
    // `format: pdf` in the current user input message.
    await exerciseAttachmentDelivery(native, 'pdf', 'kiro-doc.pdf', { protocol: 'aws-event-stream' })
  })
})
