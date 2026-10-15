import { claudeTest } from '../claude-fixtures'
import { exerciseAttachmentDelivery, exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'

claudeTest.describe('attachment support', () => {
  claudeTest('attachment-only message (no text) can be sent', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'image', 'solo.png', { protocol: 'anthropic-messages', attachmentOnly: true })
  })

  claudeTest('the model receives an image and the user row keeps its filename', async ({ native }) => {
    await exerciseImageAttachmentDelivery(native, 'history.png', { protocol: 'anthropic-messages' })
  })
})
