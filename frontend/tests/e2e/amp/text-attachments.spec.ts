import { ampTest } from '../amp-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

ampTest.describe('Amp attachments', () => {
  ampTest('delivers the contents of a text attachment to the model', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'amp-notes.txt')
  })
})
