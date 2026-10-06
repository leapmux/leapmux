import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

codebuddyTest.describe('CodeBuddy Code attachments and context usage', () => {
  codebuddyTest('the model receives a PDF attachment', async ({ native }) => {
    // CodeBuddy turns the stream-json `document` block into a Chat Completions
    // `file` part with a PDF data URI.
    await exerciseAttachmentDelivery(native, 'pdf', 'codebuddy-doc.pdf', { protocol: 'openai-chat-completions' })
  })
})
