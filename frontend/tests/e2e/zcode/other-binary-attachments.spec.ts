import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('refuses native binary input and keeps its actual bytes out of a clean model request', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
  void authenticatedZCodeWorkspace
  const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'zcode-refused.bin' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
})
