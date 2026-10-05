import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code attachments', () => {
  mimoTest('accepts a PDF attachment and carries it through the turn', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    // MiMo keeps the prompt's data URL file part, and its Chat Completions
    // serializer sends it unchanged as a `file` part with a PDF data URI.
    await exerciseAttachmentDelivery(page, modelScript, 'pdf', 'mimo-doc.pdf', { protocol: 'openai-chat-completions' })
  })
})
