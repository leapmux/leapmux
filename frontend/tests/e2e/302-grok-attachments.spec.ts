import { expect, GROK_E2E_SKIP_REASON, grokTest } from './grok-fixtures'
import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from './helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

/**
 * 302 -- Grok Build attachments.
 *
 * Grok takes every attachment kind the matrix lists (text, image, PDF, binary),
 * so no kind is refused here.
 */
grokTest.describe('Grok Build attachments', () => {
  // The file name states a word the prompt never gives, so the name in the user
  // message can only come from the attachment.
  grokTest('accepts a text attachment and carries it through the turn', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await modelScript.queue({ text: 'The note is attached.' })
    await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'grok-notes.txt' })
    await sendWithAttachment(page, modelScript.prompt('Read the attached note.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'grok-notes.txt')
    await expect(assistantBubbles(page).filter({ hasText: 'The note is attached.' }).first()).toBeVisible()
  })

  grokTest('accepts an image attachment and carries it through the turn', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await modelScript.queue({ text: 'The image is attached.' })
    await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'grok-shot.png' })
    await sendWithAttachment(page, modelScript.prompt('Describe the attached image.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'grok-shot.png')
    await expect(assistantBubbles(page).filter({ hasText: 'The image is attached.' }).first()).toBeVisible()
  })

  grokTest('accepts a PDF attachment and carries it through the turn', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await modelScript.queue({ text: 'The PDF is attached.' })
    await expectAttachmentOutcome(page, 'pdf', { supported: true, fileName: 'grok-doc.pdf' })
    await sendWithAttachment(page, modelScript.prompt('Read the attached PDF.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'grok-doc.pdf')
    await expect(assistantBubbles(page).filter({ hasText: 'The PDF is attached.' }).first()).toBeVisible()
  })

  grokTest('accepts a binary attachment and carries it through the turn', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await modelScript.queue({ text: 'The binary file is attached.' })
    await expectAttachmentOutcome(page, 'binary', { supported: true, fileName: 'grok-blob.bin' })
    await sendWithAttachment(page, modelScript.prompt('Inspect the attached file.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'grok-blob.bin')
    await expect(assistantBubbles(page).filter({ hasText: 'The binary file is attached.' }).first()).toBeVisible()
  })
})
