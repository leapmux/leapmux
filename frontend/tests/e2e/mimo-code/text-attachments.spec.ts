import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code attachments', () => {
  mimoTest('accepts a text attachment and carries it through the turn', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'mimo-notes.txt')
  })
})
