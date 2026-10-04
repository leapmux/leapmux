import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { KILO_E2E_SKIP_REASON, kiloTest } from '../kilo-fixtures'

kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')

kiloTest('keeps a refused binary attachment out of the next request', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
  void authenticatedKiloWorkspace
  const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'kilo-blob.bin' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
})
