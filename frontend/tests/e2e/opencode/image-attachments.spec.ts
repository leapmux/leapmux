import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('delivers an image attachment through the model turn', async ({ native }) => {
  await exerciseImageAttachmentDelivery(native, 'opencode-shot.png', { protocol: 'openai-chat-completions' })
})
