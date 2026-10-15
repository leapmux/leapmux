import { copilotTest } from '../copilot-fixtures'
import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'

copilotTest('delivers an image attachment to the model', async ({ native }) => {
  await exerciseImageAttachmentDelivery(native, 'copilot-shot.png', { protocol: 'openai-chat-completions' })
})
