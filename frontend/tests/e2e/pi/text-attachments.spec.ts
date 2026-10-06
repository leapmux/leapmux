import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { piTest } from '../pi-fixtures'

piTest('delivers a text attachment to the model', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'text', 'pi-notes.txt')
})
