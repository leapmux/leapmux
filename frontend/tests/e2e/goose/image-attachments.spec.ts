import { gooseTest } from '../goose-fixtures'
import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'

gooseTest('delivers an image attachment to the model', async ({ native }) => {
  await exerciseImageAttachmentDelivery(native, 'goose-shot.png')
})
