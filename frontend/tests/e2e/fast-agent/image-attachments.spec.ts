import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'

fastAgentTest.describe('attachments for Fast Agent', () => {
  fastAgentTest('delivers image attachment bytes to the model', async ({ native }) => {
    await exerciseImageAttachmentDelivery(native, 'fa-shot.png', { protocol: 'openai-chat-completions' })
  })
})
