import { grokTest } from '../grok-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

grokTest.describe('Grok Build attachments', () => {
  grokTest('accepts a text attachment and carries it through the turn', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'grok-notes.txt')
  })
})
