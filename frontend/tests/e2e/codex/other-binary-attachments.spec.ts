import { codexTest } from '../codex-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'

codexTest('refuses binary attachments and excludes their actual bytes from the next native request', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
  void authenticatedCodexWorkspace
  const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'codex-refused.bin' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
})
