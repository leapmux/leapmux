import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code attachments', () => {
  qwenTest('delivers the contents of a text attachment to the model', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'qwen-notes.txt')
  })
})
