import { cursorTest } from '../cursor-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

cursorTest('delivers a text attachment to the model', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'text', 'cursor-notes.txt')
})
