import { COPILOT_E2E_SKIP_REASON, copilotTest } from '../copilot-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest('keeps a refused PDF attachment out of the next request', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
  void authenticatedCopilotWorkspace
  const rejected = await expectAttachmentOutcome(page, 'pdf', { supported: false, fileName: 'copilot-doc.pdf' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
})
