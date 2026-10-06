import { ampTest } from '../amp-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

ampTest.describe('Amp attachments', () => {
  ampTest('delivers an image attachment to the model', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    void authenticatedAmpWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'amp-shot.png')
  })
})
