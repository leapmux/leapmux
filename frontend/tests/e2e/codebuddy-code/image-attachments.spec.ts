import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

codebuddyTest.describe('CodeBuddy Code attachments and context usage', () => {
  codebuddyTest('the model receives an image attachment', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'image', 'shot.png', { protocol: 'openai-chat-completions' })
  })
})
