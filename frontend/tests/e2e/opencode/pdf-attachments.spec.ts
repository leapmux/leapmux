import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('accepts a PDF attachment and carries it through the turn', async ({ native }) => {
  // OpenCode keeps the ACP blob as a data URL file part, and its Chat Completions
  // serializer sends it unchanged as a `file` part with a PDF data URI.
  await exerciseAttachmentDelivery(native, 'pdf', 'opencode-doc.pdf', { protocol: 'openai-chat-completions' })
})
