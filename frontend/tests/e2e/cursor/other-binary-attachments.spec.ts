import { cursorTest } from '../cursor-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'

cursorTest('other-binary-attachments: keeps refused PDF and binary attachments out of the next request', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
  void authenticatedCursorWorkspace
  const pdf = await expectAttachmentOutcome(page, 'pdf', { supported: false, fileName: 'cursor-doc.pdf' })
  const binary = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'cursor-blob.bin' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [pdf, binary])
})
