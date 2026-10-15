import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { museTest } from '../muse-fixtures'

museTest('delivers actual image bytes to the native model', async ({ native }) => {
  await exerciseImageAttachmentDelivery(native, 'muse-image.png')
})
