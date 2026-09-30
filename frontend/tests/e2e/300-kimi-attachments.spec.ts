import { exerciseAttachmentDelivery, expectRefusedAttachmentsAbsent } from './helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from './helpers/attachments'
import { KIMI_E2E_SKIP_REASON, kimiTest } from './kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

/**
 * 300 -- Kimi Code attachments.
 *
 * Kimi takes text and image attachments (matrix). It takes no PDF and no other
 * binary kind, so the matrix marks those cells false and this file covers only
 * the two it supports.
 */
kimiTest.describe('Kimi Code attachments', () => {
  kimiTest('accepts a text attachment and carries it through the turn', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'kimi-notes.txt')
  })

  kimiTest('accepts an image attachment and carries it through the turn', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'kimi-shot.png')
  })

  kimiTest('refuses a PDF and a binary file', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    const pdf = await expectAttachmentOutcome(page, 'pdf', { supported: false })
    const binary = await expectAttachmentOutcome(page, 'binary', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [pdf, binary])
  })
})
