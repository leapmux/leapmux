import { copilotTest } from '../copilot-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'

copilotTest('keeps a refused PDF attachment out of the next request', async ({ authenticatedCopilotWorkspace, page, modelScript }) => {
  void authenticatedCopilotWorkspace
  const rejected = await expectAttachmentOutcome(page, 'pdf', { supported: false, fileName: 'copilot-doc.pdf' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
})
