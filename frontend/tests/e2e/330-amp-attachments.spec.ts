import { AMP_E2E_SKIP_REASON, ampTest } from './amp-fixtures'
import { exerciseAttachmentDelivery } from './helpers/attachmentModelProbe'

ampTest.skip(!!AMP_E2E_SKIP_REASON, AMP_E2E_SKIP_REASON || '')

ampTest.describe('Amp attachments', () => {
  ampTest('delivers the contents of a text attachment to the model', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    void authenticatedAmpWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'amp-notes.txt', { readyGroup: 'agent_mode' })
  })

  ampTest('delivers an image attachment to the model', async ({ authenticatedAmpWorkspace, page, modelScript }) => {
    void authenticatedAmpWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'amp-shot.png', { readyGroup: 'agent_mode' })
  })
})
