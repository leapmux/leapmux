import { commandCodeTest } from '../command-code-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'

commandCodeTest('refuses the unsupported native binary attachment and keeps it out of the next request', async ({ commandCodeWorkspace, page, modelScript }) => {
  void commandCodeWorkspace
  const path = await expectAttachmentOutcome(page, 'binary', { supported: false })
  await expectRefusedAttachmentsAbsent(page, modelScript, [path], { text: 'The clean native prompt completed.' })
})
