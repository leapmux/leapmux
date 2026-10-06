import { gooseTest } from '../goose-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

gooseTest('delivers an image attachment to the model', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'image', 'goose-shot.png')
})
