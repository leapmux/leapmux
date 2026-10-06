import { droidTest } from '../droid-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

droidTest.describe('Factory Droid attachments', () => {
  droidTest('delivers text attachment bytes to the model', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'droid-notes.txt')
  })
})
