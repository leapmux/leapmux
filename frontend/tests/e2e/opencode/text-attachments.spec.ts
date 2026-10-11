import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('accepts a text attachment and carries it through the turn', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'text', 'opencode-notes.txt', { protocol: 'openai-chat-completions' })
})
