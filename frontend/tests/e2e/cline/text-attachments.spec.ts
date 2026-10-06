import { clineTest } from '../cline-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

clineTest.describe('Cline attachments', () => {
  clineTest('accepts a text attachment and carries it through the turn', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'cline-notes.txt')
  })
})
