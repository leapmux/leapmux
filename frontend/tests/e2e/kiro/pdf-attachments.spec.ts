import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest.describe('Kiro attachments', () => {
  kiroTest('accepts a PDF attachment and carries it through the turn', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'pdf', 'kiro-doc.pdf')
  })
})
