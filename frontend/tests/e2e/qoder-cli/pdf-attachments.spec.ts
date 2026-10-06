import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI attachments and context usage', () => {
  qoderTest('refuses a PDF attachment that its native input cannot carry', async ({ native }) => {
    await exerciseAttachmentRefusal(native, 'pdf')
  })
})
