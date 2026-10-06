import { commandCodeTest } from '../command-code-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

commandCodeTest('sends actual text attachment bytes through the native input path', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'text', 'notes.txt', { protocol: 'openai-chat-completions' })
})
