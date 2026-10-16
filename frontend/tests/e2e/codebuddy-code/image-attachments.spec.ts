import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'

codebuddyTest.describe('attachments and context usage for CodeBuddy Code', () => {
  codebuddyTest('the model receives an image attachment', async ({ native }) => {
    await exerciseImageAttachmentDelivery(native, 'shot.png', { protocol: 'openai-chat-completions' })
  })
})
