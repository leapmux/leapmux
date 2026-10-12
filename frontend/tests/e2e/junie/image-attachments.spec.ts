import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { junieTest } from '../junie-fixtures'

junieTest.describe('Junie attachments and context usage', () => {
  junieTest('delivers image attachment bytes to the model', async ({ native }) => {
    // Junie decodes the PNG with Java ImageIO and writes a new PNG (ImageContentHelper.adjustImageData), so the pixels
    // stay and the bytes change. The current user turn must carry an image/png part with the four quadrant colors.
    await exerciseAttachmentDelivery(native, 'image', 'jshot.png', { protocol: 'openai-chat-completions', transcodedImageType: 'image/png' })
  })
})
