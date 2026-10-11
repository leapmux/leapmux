import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code attachments', () => {
  qwenTest('delivers a PDF attachment to the model', async ({ native }) => {
    // Qwen Code turns the ACP blob into inline data and sends the original bytes
    // as a Chat Completions `file` part, because the model declares PDF input.
    await exerciseAttachmentDelivery(native, 'pdf', 'qwen-doc.pdf', { protocol: 'openai-chat-completions' })
  })
})
