import { ampTest } from '../amp-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

ampTest.describe('Amp attachments', () => {
  ampTest('delivers the contents of a text attachment to the model', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    void authenticatedAmpWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'amp-notes.txt')
  })
})
