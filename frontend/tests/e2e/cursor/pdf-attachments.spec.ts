import { CURSOR_E2E_SKIP_REASON, cursorTest } from '../cursor-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest('keeps refused PDF and binary attachments out of the next request', async ({ authenticatedCursorWorkspace, page, modelScript }) => {
  void authenticatedCursorWorkspace
  const pdf = await expectAttachmentOutcome(page, 'pdf', { supported: false, fileName: 'cursor-doc.pdf' })
  const binary = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'cursor-blob.bin' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [pdf, binary])
})
