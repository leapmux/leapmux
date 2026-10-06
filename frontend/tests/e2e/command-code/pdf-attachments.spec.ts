import { commandCodeTest } from '../command-code-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'

commandCodeTest('refuses the unsupported native pdf attachment and keeps it out of the next request', async ({ authenticatedCommandCodeWorkspace, page, modelScript }) => {
  void authenticatedCommandCodeWorkspace
  const path = await expectAttachmentOutcome(page, 'pdf', { supported: false })
  await expectRefusedAttachmentsAbsent(page, modelScript, [path], { text: 'The clean native prompt completed.' })
})
