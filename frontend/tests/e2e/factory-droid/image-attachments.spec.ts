import { droidTest } from '../droid-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

droidTest.describe('Factory Droid attachments', () => {
  droidTest('delivers image attachment bytes to the model', async ({ native }) => {
    // Droid decodes the PNG and encodes it again with pngjs before the request,
    // so the part holds a new RGBA PNG with the same four colors.
    await exerciseAttachmentDelivery(native, 'image', 'droid-shot.png', { protocol: 'openai-chat-completions', transcodedImageType: 'image/png' })
  })
})
