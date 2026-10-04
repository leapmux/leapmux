import { codexTest } from '../codex-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'

codexTest('refuses a PDF and excludes its actual bytes from the next native request', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
  void authenticatedCodexWorkspace
  const rejected = await expectAttachmentOutcome(page, 'pdf', { supported: false, fileName: 'codex-refused.pdf' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
})
