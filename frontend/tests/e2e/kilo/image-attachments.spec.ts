import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { kiloTest } from '../kilo-fixtures'

kiloTest('delivers an image attachment to the model', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'image', 'kilo-shot.png')
})
