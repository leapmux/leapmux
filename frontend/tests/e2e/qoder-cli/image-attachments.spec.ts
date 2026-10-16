import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('attachments and context usage for Qoder CLI', () => {
  qoderTest('delivers image attachment bytes to the model', async ({ native }) => {
    await exerciseImageAttachmentDelivery(native, 'qoder-shot.png', { protocol: 'openai-chat-completions' })
  })
})
