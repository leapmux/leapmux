import { fastAgentTest } from '../fastagent-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'

fastAgentTest.describe('Fast Agent attachments', () => {
  fastAgentTest('refuses another binary attachment before a model request', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false, readyGroup: 'permissionMode' })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })
})
