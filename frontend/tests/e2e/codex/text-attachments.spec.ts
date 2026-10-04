import { codexTest } from '../codex-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

codexTest.describe('Codex attachment support', () => {
  codexTest('delivers a text attachment to the model', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'codex-notes.txt')
  })
})
