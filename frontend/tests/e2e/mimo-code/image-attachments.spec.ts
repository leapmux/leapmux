import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code attachments', () => {
  mimoTest('accepts an image attachment and carries it through the turn', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'mimo-shot.png')
  })
})
