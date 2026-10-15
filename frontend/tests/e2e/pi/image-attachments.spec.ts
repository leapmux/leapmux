import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { piTest } from '../pi-fixtures'

piTest('delivers an image attachment to the model', async ({ native }) => {
  await exerciseImageAttachmentDelivery(native, 'pi-shot.png')
})
