import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code attachments', () => {
  qwenTest('refuses a binary attachment before the model request', async ({ native }) => {
    await exerciseAttachmentRefusal(native, 'binary')
  })
})
