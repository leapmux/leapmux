import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI attachments and context usage', () => {
  qoderTest('refuses another binary attachment that its native input cannot carry', async ({ authenticatedQoderWorkspace, page, modelScript }) => {
    void authenticatedQoderWorkspace
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })
})
