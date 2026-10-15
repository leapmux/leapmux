import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { kiloTest } from '../kilo-fixtures'

kiloTest('delivers an image attachment to the model', async ({ native }) => {
  await exerciseImageAttachmentDelivery(native, 'kilo-shot.png')
})
