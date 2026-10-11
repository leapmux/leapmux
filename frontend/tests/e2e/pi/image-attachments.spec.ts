import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { piTest } from '../pi-fixtures'

piTest('delivers an image attachment to the model', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'image', 'pi-shot.png')
})
