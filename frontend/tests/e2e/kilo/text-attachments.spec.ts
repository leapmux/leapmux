import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { kiloTest } from '../kilo-fixtures'

kiloTest('delivers a text attachment to the model', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'text', 'kilo-notes.txt')
})
