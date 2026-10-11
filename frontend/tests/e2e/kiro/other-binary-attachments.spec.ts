import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { kiroTest } from '../kiro-fixtures'

kiroTest.describe('Kiro attachments', () => {
  kiroTest('refuses a binary file', async ({ native }) => {
    await exerciseAttachmentRefusal(native, 'binary')
  })
})
