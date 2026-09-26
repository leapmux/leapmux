import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from './helpers/ui'
import { expect, KIMI_E2E_SKIP_REASON, kimiTest } from './kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

/**
 * 300 -- Kimi Code attachments.
 *
 * Kimi takes text and image attachments (matrix). It takes no PDF and no other
 * binary kind, so the matrix marks those cells false and this file covers only
 * the two it supports.
 */
kimiTest.describe('Kimi Code attachments', () => {
  // The file name states a word the prompt never gives, so the name in the user
  // message can only come from the attachment.
  kimiTest('accepts a text attachment and carries it through the turn', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await modelScript.queue({ text: 'The note is attached.' })
    await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'kimi-notes.txt' })
    await sendWithAttachment(page, modelScript.prompt('Read the attached note.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'kimi-notes.txt')
    await expect(assistantBubbles(page).filter({ hasText: 'The note is attached.' }).first()).toBeVisible()
  })

  kimiTest('accepts an image attachment and carries it through the turn', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await modelScript.queue({ text: 'The image is attached.' })
    await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'kimi-shot.png' })
    await sendWithAttachment(page, modelScript.prompt('Describe the attached image.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'kimi-shot.png')
    await expect(assistantBubbles(page).filter({ hasText: 'The image is attached.' }).first()).toBeVisible()
  })

  kimiTest('refuses a PDF and a binary file', async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await expectAttachmentOutcome(page, 'pdf', { supported: false })
    await expectAttachmentOutcome(page, 'binary', { supported: false })
  })
})
