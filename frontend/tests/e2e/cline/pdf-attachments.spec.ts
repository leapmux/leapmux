import { clineTest } from '../cline-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

clineTest.describe('Cline attachments', () => {
  clineTest('refuses a PDF and a binary file', async ({ native }) => {
    await exerciseAttachmentRefusal(native, 'pdf', 'binary')
  })
})
