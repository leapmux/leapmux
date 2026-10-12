import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { museTest } from '../muse-fixtures'

museTest('delivers the complete text attachment to the native model', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'text', 'muse-notes.txt')
})
