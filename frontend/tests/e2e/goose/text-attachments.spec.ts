import { gooseTest } from '../goose-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

gooseTest('delivers a text attachment to the model', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'text', 'goose-notes.txt')
})
