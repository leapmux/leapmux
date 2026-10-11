import { copilotTest } from '../copilot-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

copilotTest('delivers an image attachment to the model', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'image', 'copilot-shot.png', { protocol: 'openai-chat-completions' })
})
