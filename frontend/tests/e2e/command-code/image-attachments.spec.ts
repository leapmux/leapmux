import { commandCodeTest } from '../command-code-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

commandCodeTest('sends actual image attachment bytes through the native input path', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'image', 'shot.png', { protocol: 'openai-chat-completions' })
})
