import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { junieTest } from '../junie-fixtures'

junieTest.describe('Junie attachments and context usage', () => {
  junieTest('refuses a PDF attachment before a model request', async ({ native }) => {
    await exerciseAttachmentRefusal(native, 'pdf')
  })
})
