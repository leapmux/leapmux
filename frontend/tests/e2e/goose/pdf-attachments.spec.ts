import { gooseTest } from '../goose-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'

gooseTest('keeps refused PDF and binary attachments out of the next request', async ({ authenticatedGooseWorkspace, page, modelScript }) => {
  void authenticatedGooseWorkspace
  const pdf = await expectAttachmentOutcome(page, 'pdf', { supported: false, fileName: 'goose-doc.pdf' })
  const binary = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'goose-blob.bin' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [pdf, binary])
})
