import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

codebuddyTest.describe('CodeBuddy Code attachments and context usage', () => {
  codebuddyTest('the model receives a text attachment', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'notes.txt', { protocol: 'openai-chat-completions' })
  })
})
