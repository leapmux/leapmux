import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest.describe('Qwen Code attachments', () => {
  qwenTest('refuses a binary attachment before the model request', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })
})
