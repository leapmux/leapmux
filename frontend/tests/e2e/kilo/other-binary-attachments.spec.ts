import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { kiloTest } from '../kilo-fixtures'

kiloTest('keeps a refused binary attachment out of the next request', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
  void authenticatedKiloWorkspace
  const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'kilo-blob.bin' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
})
