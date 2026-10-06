import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

fastAgentTest.describe('Fast Agent attachments', () => {
  fastAgentTest('delivers PDF attachment bytes to the model', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    // fast-agent sends the ACP blob unchanged as a Chat Completions `file` part
    // with a PDF data URI.
    await exerciseAttachmentDelivery(page, modelScript, 'pdf', 'fa-doc.pdf', { protocol: 'openai-chat-completions' })
  })
})
