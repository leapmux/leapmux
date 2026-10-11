import { clineTest } from '../cline-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

clineTest.describe('Cline attachments', () => {
  clineTest('accepts a text attachment and carries it through the turn', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'cline-notes.txt')
  })
})
