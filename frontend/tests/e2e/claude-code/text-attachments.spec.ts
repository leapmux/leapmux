import { claudeTest } from '../claude-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { nativeContext } from './scenarios'

claudeTest.describe('Attachment Support', () => {
  claudeTest('a text file reaches the model', async ({ page, modelScript, leapmuxServer, authenticatedWorkspace }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedWorkspace.workspaceId })
    await exerciseAttachmentDelivery(context, 'text', 'notes.txt', { protocol: 'anthropic-messages' })
  })
})
