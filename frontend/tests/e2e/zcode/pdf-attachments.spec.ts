import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('refuses native pdf input and keeps its actual bytes out of a clean model request', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
  void authenticatedZCodeWorkspace
  const rejected = await expectAttachmentOutcome(page, 'pdf', { supported: false, fileName: 'zcode-refused.pdf' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
})
