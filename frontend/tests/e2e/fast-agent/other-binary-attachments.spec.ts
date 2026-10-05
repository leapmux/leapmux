import { FAST_AGENT_E2E_SKIP_REASON, fastAgentTest } from '../fastagent-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'

fastAgentTest.describe('Fast Agent attachments', () => {
  fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

  fastAgentTest('refuses another binary attachment before a model request', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false, readyGroup: 'permissionMode' })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })
})
