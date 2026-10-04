import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest.describe('Cline attachments', () => {
  clineTest('accepts a text attachment and carries it through the turn', async ({ authenticatedClineWorkspace, page, modelScript }) => {
    void authenticatedClineWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'cline-notes.txt')
  })
})
