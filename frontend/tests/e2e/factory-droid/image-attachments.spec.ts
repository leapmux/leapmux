import { droidTest } from '../droid-fixtures'
import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'

droidTest.describe('attachments for Factory Droid', () => {
  droidTest('delivers image attachment bytes to the model', async ({ native }) => {
    // Droid decodes the PNG and encodes it again with pngjs before the request,
    // so the part holds a new RGBA PNG with the same four colors.
    await exerciseImageAttachmentDelivery(native, 'droid-shot.png', { protocol: 'openai-chat-completions', transcodedImageType: 'image/png' })
  })
})
