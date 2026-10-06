import { droidTest } from '../droid-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'

droidTest.describe('Factory Droid attachments', () => {
  droidTest('refuses a PDF attachment before a model request', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    const rejected = await expectAttachmentOutcome(page, 'pdf', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })
})
