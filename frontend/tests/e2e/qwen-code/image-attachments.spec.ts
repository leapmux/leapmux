import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code attachments', () => {
  qwenTest('delivers an image attachment to the model', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'image', 'qwen-shot.png', { protocol: 'openai-chat-completions' })
  })
})
