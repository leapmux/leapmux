import { droidTest } from '../droid-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

droidTest.describe('Factory Droid attachments', () => {
  droidTest('delivers text attachment bytes to the model', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'droid-notes.txt')
  })
})
