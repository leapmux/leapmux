import { droidTest } from '../droid-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

droidTest.describe('Factory Droid attachments', () => {
  droidTest('refuses a PDF attachment before a model request', async ({ native }) => {
    await exerciseAttachmentRefusal(native, 'pdf')
  })
})
