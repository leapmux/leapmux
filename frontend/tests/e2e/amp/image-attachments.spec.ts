import { ampTest } from '../amp-fixtures'
import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'

ampTest.describe('attachments for Amp', () => {
  ampTest('delivers an image attachment to the model', async ({ native }) => {
    await exerciseImageAttachmentDelivery(native, 'amp-shot.png')
  })
})
