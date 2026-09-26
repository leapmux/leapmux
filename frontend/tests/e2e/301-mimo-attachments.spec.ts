import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from './helpers/ui'
import { expect, MIMO_E2E_SKIP_REASON, mimoTest } from './mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

/**
 * 301 -- MiMo Code attachments.
 *
 * MiMo takes text, image and PDF attachments (matrix). It takes no other binary
 * kind, so the matrix marks that cell false.
 */
mimoTest.describe('MiMo Code attachments', () => {
  // The file name states a word the prompt never gives, so the name in the user
  // message can only come from the attachment.
  mimoTest('accepts a text attachment and carries it through the turn', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue({ text: 'The note is attached.' })
    await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'mimo-notes.txt' })
    await sendWithAttachment(page, modelScript.prompt('Read the attached note.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'mimo-notes.txt')
    await expect(assistantBubbles(page).filter({ hasText: 'The note is attached.' }).first()).toBeVisible()
  })

  mimoTest('accepts an image attachment and carries it through the turn', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue({ text: 'The image is attached.' })
    await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'mimo-shot.png' })
    await sendWithAttachment(page, modelScript.prompt('Describe the attached image.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'mimo-shot.png')
    await expect(assistantBubbles(page).filter({ hasText: 'The image is attached.' }).first()).toBeVisible()
  })

  mimoTest('accepts a PDF attachment and carries it through the turn', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue({ text: 'The PDF is attached.' })
    await expectAttachmentOutcome(page, 'pdf', { supported: true, fileName: 'mimo-doc.pdf' })
    await sendWithAttachment(page, modelScript.prompt('Read the attached PDF.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'mimo-doc.pdf')
    await expect(assistantBubbles(page).filter({ hasText: 'The PDF is attached.' }).first()).toBeVisible()
  })

  mimoTest('refuses a binary file', async ({ authenticatedMiMoWorkspace, page }) => {
    void authenticatedMiMoWorkspace
    await expectAttachmentOutcome(page, 'binary', { supported: false })
  })
})
