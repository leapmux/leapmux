import { FAST_AGENT_E2E_SKIP_REASON, fastAgentTest } from '../fastagent-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

fastAgentTest.describe('Fast Agent attachments', () => {
  fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

  fastAgentTest('delivers image attachment bytes to the model', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'fa-shot.png')
  })
})
