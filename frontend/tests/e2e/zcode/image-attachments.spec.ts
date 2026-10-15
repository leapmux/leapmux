import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('delivers an image attachment to the model', async ({ native }) => {
  await exerciseImageAttachmentDelivery(native, 'zcode-shot.png')
})
