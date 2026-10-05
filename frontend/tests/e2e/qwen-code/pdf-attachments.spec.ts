import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest.describe('Qwen Code attachments', () => {
  qwenTest('delivers a PDF attachment to the model', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    // Qwen Code turns the ACP blob into inline data and sends the original bytes
    // as a Chat Completions `file` part, because the model declares PDF input.
    await exerciseAttachmentDelivery(page, modelScript, 'pdf', 'qwen-doc.pdf', { protocol: 'openai-chat-completions' })
  })
})
