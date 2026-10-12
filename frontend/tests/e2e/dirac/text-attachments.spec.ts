import { diracTest } from '../dirac-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

diracTest.describe('Dirac attachments', () => {
  diracTest('accepts a text attachment and carries it through the turn', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'dirac-notes.txt')
  })
})
