import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code attachments', () => {
  mimoTest('accepts a PDF attachment and carries it through the turn', async ({ native }) => {
    // MiMo keeps the prompt's data URL file part, and its Chat Completions
    // serializer sends it unchanged as a `file` part with a PDF data URI.
    await exerciseAttachmentDelivery(native, 'pdf', 'mimo-doc.pdf', { protocol: 'openai-chat-completions' })
  })
})
