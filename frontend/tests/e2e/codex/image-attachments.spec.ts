import { codexTest } from '../codex-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

codexTest.describe('Codex attachment support', () => {
  codexTest('delivers an image attachment to the model', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'image', 'codex-shot.png', { protocol: 'openai-responses' })
  })
})
