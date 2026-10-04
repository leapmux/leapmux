import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('refuses native image input and keeps its actual bytes out of a clean model request', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
  void authenticatedReasonixWorkspace
  const rejected = await expectAttachmentOutcome(page, 'image', { supported: false, fileName: 'reasonix-refused.png' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
})
