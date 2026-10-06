import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('Kimi Code attachments', () => {
  kimiTest('accepts a text attachment and carries it through the turn', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'kimi-notes.txt')
  })
})
