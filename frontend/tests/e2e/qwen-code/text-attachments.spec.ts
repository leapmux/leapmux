import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code attachments', () => {
  qwenTest('delivers the contents of a text attachment to the model', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'qwen-notes.txt')
  })
})
