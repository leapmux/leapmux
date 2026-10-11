import { ampTest } from '../amp-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

ampTest.describe('Amp attachments', () => {
  ampTest('delivers an image attachment to the model', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'image', 'amp-shot.png')
  })
})
