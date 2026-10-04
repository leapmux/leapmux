import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest } from '../codewhale-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest.describe('Codewhale attachments', () => {
  codewhaleTest('delivers the contents of a text attachment to the model', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'codewhale-notes.txt')
  })
})
