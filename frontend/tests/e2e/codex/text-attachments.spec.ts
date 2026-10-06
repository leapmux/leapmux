import { codexTest } from '../codex-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

codexTest.describe('Codex attachment support', () => {
  codexTest('delivers a text attachment to the model', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'codex-notes.txt')
  })
})
