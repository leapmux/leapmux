import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('delivers native image bytes with a prompt and with an empty composer', async ({ native }) => {
  await exerciseImageAttachmentDelivery(native, 'reasonix-image.png')
})
