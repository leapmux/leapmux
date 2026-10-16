import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { qwenTest } from '../qwen-fixtures'

qwenTest.describe('attachments for Qwen Code', () => {
  qwenTest('delivers an image attachment to the model', async ({ native }) => {
    await exerciseImageAttachmentDelivery(native, 'qwen-shot.png', { protocol: 'openai-chat-completions' })
  })
})
