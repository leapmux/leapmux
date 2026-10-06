import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { kiroTest } from '../kiro-fixtures'

kiroTest.describe('Kiro attachments', () => {
  kiroTest('accepts a text attachment and carries it through the turn', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'kiro-notes.txt')
  })
})
