import { exerciseAttachmentDelivery, expectRefusedAttachmentsAbsent } from './helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from './helpers/attachments'
import { MIMO_E2E_SKIP_REASON, mimoTest } from './mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

/**
 * 301 -- MiMo Code attachments.
 *
 * MiMo takes text, image and PDF attachments (matrix). It takes no other binary
 * kind, so the matrix marks that cell false.
 */
mimoTest.describe('MiMo Code attachments', () => {
  mimoTest('accepts a text attachment and carries it through the turn', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'mimo-notes.txt')
  })

  mimoTest('accepts an image attachment and carries it through the turn', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'mimo-shot.png')
  })

  mimoTest('accepts a PDF attachment and carries it through the turn', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'pdf', 'mimo-doc.pdf')
  })

  mimoTest('refuses a binary file', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })
})
