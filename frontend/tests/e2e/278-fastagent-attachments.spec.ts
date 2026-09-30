import { FAST_AGENT_E2E_SKIP_REASON, fastAgentTest } from './fastagent-fixtures'
import { exerciseAttachmentDelivery, expectRefusedAttachmentsAbsent } from './helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from './helpers/attachments'

fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

/**
 * 278 — Fast Agent attachments.
 *
 * Fast Agent converts the ACP attachment blocks into model content. The
 * tests check the model request for text, image, and PDF bytes.
 */
fastAgentTest.describe('Fast Agent attachments', () => {
  fastAgentTest('delivers text attachment bytes to the model', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'fa-notes.txt')
  })

  fastAgentTest('delivers image attachment bytes to the model', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'fa-shot.png')
  })

  fastAgentTest('delivers PDF attachment bytes to the model', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'pdf', 'fa-doc.pdf')
  })

  fastAgentTest('refuses another binary attachment before a model request', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })
})
