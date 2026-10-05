import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest.describe('Qwen Code attachments', () => {
  qwenTest('delivers an image attachment to the model', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'qwen-shot.png', { protocol: 'openai-chat-completions' })
  })
})
