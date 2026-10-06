import { DROID_TITLE_RULE, droidTest } from '../droid-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

droidTest.describe('Factory Droid attachments', () => {
  droidTest('delivers text attachment bytes to the model', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    await modelScript.rule(DROID_TITLE_RULE)
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'droid-notes.txt')
  })
})
