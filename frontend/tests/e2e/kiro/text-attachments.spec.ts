import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { kiroTest } from '../kiro-fixtures'

kiroTest.describe('Kiro attachments', () => {
  kiroTest('accepts a text attachment and carries it through the turn', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'kiro-notes.txt')
  })
})
