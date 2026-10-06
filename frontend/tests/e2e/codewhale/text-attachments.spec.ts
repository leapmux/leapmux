import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

codewhaleTest.describe('Codewhale attachments', () => {
  codewhaleTest('delivers the contents of a text attachment to the model', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'codewhale-notes.txt')
  })
})
