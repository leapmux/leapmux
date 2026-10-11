import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { kiloTest } from '../kilo-fixtures'

kiloTest('delivers a pdf attachment to the model', async ({ native }) => {
  // Kilo keeps the ACP blob as a data URL file part, and its Chat Completions
  // serializer sends it unchanged as a `file` part with a PDF data URI.
  await exerciseAttachmentDelivery(native, 'pdf', 'kilo-doc.pdf', { protocol: 'openai-chat-completions' })
})
