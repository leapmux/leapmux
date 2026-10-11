import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('delivers an image attachment to the model', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'image', 'zcode-shot.png')
})
