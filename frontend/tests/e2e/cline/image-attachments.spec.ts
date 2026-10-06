import { clineTest } from '../cline-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

clineTest.describe('Cline attachments', () => {
  clineTest('accepts an image attachment and carries it through the turn', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'cline-shot.png')
  })
})
