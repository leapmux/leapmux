import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

fastAgentTest.describe('Fast Agent attachments', () => {
  fastAgentTest('delivers image attachment bytes to the model', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'image', 'fa-shot.png', { protocol: 'openai-chat-completions' })
  })
})
