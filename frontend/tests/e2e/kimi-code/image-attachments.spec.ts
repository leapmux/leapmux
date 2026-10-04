import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

kimiTest.describe('Kimi Code attachments', () => {
  kimiTest('accepts an image attachment and carries it through the turn', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'kimi-shot.png')
  })
})
