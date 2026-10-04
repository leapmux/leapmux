import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { piTest } from '../pi-fixtures'

piTest('refuses native binary input and keeps its actual bytes out of a clean model request', async ({ authenticatedPiWorkspace, page, modelScript }) => {
  void authenticatedPiWorkspace
  const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'pi-refused.bin' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
})
