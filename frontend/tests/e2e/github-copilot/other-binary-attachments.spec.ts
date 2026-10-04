import { COPILOT_E2E_SKIP_REASON, copilotTest } from '../copilot-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest('keeps a refused binary attachment out of the next request', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
  void authenticatedCopilotWorkspace
  const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'copilot-blob.bin' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
})
