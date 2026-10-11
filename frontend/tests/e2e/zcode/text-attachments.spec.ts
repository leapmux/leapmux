import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('delivers a text attachment to the model', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'text', 'zcode-notes.txt')
})
