import { droidTest } from '../droid-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

droidTest.describe('attachments for Factory Droid', () => {
  droidTest('refuses a PDF attachment before a model request', async ({ native }) => {
    await exerciseAttachmentRefusal(native, 'pdf')
  })
})

// Track this LeapMux limitation: https://github.com/leapmux/leapmux/issues/510.
