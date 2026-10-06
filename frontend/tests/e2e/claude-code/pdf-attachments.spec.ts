import { claudeTest } from '../claude-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { nativeContext } from './scenarios'

claudeTest.describe('Attachment Support', () => {
  // Claude Code is one of the providers the matrix marks for PDF attachments.
  // The accept attribute and the kind classifier both have unit coverage; this
  // is the composer path end to end.
  claudeTest('a PDF reaches the model', async ({ page, modelScript, leapmuxServer, authenticatedWorkspace }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedWorkspace.workspaceId })
    // Claude Code forwards the stream-json `document` block unchanged into the
    // user message of its Messages request.
    await exerciseAttachmentDelivery(context, 'pdf', 'spec.pdf', { protocol: 'anthropic-messages' })
  })
})
