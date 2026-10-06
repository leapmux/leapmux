import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

codewhaleTest.describe('Codewhale attachments', () => {
  codewhaleTest('delivers the contents of a text attachment to the model', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'codewhale-notes.txt')
  })
})
