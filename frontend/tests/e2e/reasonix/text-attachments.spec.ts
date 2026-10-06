import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('delivers a text attachment to the model', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'text', 'reasonix-notes.txt')
})
