import { exerciseAttachmentDelivery, expectRefusedAttachmentsAbsent } from './helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from './helpers/attachments'
import { QWEN_E2E_SKIP_REASON, qwenTest } from './qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest.describe('Qwen Code attachments', () => {
  qwenTest('delivers the contents of a text attachment to the model', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'qwen-notes.txt')
  })

  qwenTest('delivers an image attachment to the model', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'qwen-shot.png')
  })

  qwenTest('delivers a PDF attachment to the model', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'pdf', 'qwen-doc.pdf')
  })

  qwenTest('refuses a binary attachment before the model request', async ({ authenticatedQwenWorkspace, page, modelScript }) => {
    void authenticatedQwenWorkspace
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })
})
