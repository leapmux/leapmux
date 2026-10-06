import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('delivers an image attachment through the model turn', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'image', 'opencode-shot.png', { protocol: 'openai-chat-completions' })
})
