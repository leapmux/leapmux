import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('Kimi Code attachments', () => {
  kimiTest('refuses a PDF and a binary file', async ({ native }) => {
    await exerciseAttachmentRefusal(native, 'pdf', 'binary')
  })
})
