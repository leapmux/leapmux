import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest.describe('attachments for Oh My Pi', () => {
  ohMyPiTest('delivers an image attachment to the model', async ({ native }) => {
    // Oh My Pi raises an edge below 200 px to its minimum and keeps the smallest of PNG, JPEG, and WebP
    // (coding-agent/src/utils/image-resize.ts). For the 16x16 fixture that is a 200x200 WebP.
    await exerciseImageAttachmentDelivery(native, 'omp-shot.png', { protocol: 'openai-chat-completions', transcodedImageType: 'image/webp' })
  })
})
