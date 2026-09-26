import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from './helpers/ui'
import { expect, KIRO_E2E_SKIP_REASON, kiroTest } from './kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

/**
 * 303 -- Kiro attachments.
 *
 * Kiro takes text, image and PDF attachments (matrix). It takes no other binary
 * kind, so the matrix marks that cell false.
 */
kiroTest.describe('Kiro attachments', () => {
  // The file name states a word the prompt never gives, so the name in the user
  // message can only come from the attachment.
  kiroTest('accepts a text attachment and carries it through the turn', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    await modelScript.queue({ text: 'The note is attached.' })
    await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'kiro-notes.txt' })
    await sendWithAttachment(page, modelScript.prompt('Read the attached note.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'kiro-notes.txt')
    await expect(assistantBubbles(page).filter({ hasText: 'The note is attached.' }).first()).toBeVisible()
  })

  kiroTest('accepts an image attachment and carries it through the turn', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    await modelScript.queue({ text: 'The image is attached.' })
    await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'kiro-shot.png' })
    await sendWithAttachment(page, modelScript.prompt('Describe the attached image.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'kiro-shot.png')
    await expect(assistantBubbles(page).filter({ hasText: 'The image is attached.' }).first()).toBeVisible()
  })

  kiroTest('accepts a PDF attachment and carries it through the turn', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    await modelScript.queue({ text: 'The PDF is attached.' })
    await expectAttachmentOutcome(page, 'pdf', { supported: true, fileName: 'kiro-doc.pdf' })
    await sendWithAttachment(page, modelScript.prompt('Read the attached PDF.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'kiro-doc.pdf')
    await expect(assistantBubbles(page).filter({ hasText: 'The PDF is attached.' }).first()).toBeVisible()
  })

  kiroTest('refuses a binary file', async ({ authenticatedKiroWorkspace, page }) => {
    void authenticatedKiroWorkspace
    await expectAttachmentOutcome(page, 'binary', { supported: false })
  })
})
