import { codexTest } from '../codex-fixtures'
import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'

codexTest.describe('attachment support for Codex', () => {
  codexTest('delivers an image attachment to the model', async ({ native }) => {
    await exerciseImageAttachmentDelivery(native, 'codex-shot.png', { protocol: 'openai-responses' })
  })
})
