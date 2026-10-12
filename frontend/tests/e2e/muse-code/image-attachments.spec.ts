import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { museTest } from '../muse-fixtures'

museTest('delivers actual image bytes to the native model', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'image', 'muse-image.png')
})
