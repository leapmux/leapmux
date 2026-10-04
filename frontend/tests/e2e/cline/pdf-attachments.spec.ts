import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'

clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest.describe('Cline attachments', () => {
  clineTest('refuses a PDF and a binary file', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    const pdf = await expectAttachmentOutcome(page, 'pdf', { supported: false })
    const binary = await expectAttachmentOutcome(page, 'binary', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [pdf, binary])
  })
})
