import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('refuses native pdf input and keeps its actual bytes out of a clean model request', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
  void authenticatedReasonixWorkspace
  const rejected = await expectAttachmentOutcome(page, 'pdf', { supported: false, fileName: 'reasonix-refused.pdf' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
})
