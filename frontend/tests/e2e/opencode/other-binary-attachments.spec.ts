import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('refuses a binary attachment before it enters the queue', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
  void authenticatedOpencodeWorkspace
  const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false, fileName: 'opencode-blob.bin' })
  await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
})
