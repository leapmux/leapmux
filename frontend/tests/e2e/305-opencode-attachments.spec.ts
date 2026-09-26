import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from './helpers/ui'
import { expect, OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

/**
 * 305 -- OpenCode attachments.
 *
 * The composer offers every kind the matrix lists (text, image, PDF, binary).
 * Text and PDF complete a turn. The shipped OpenCode ACP adapter fails the
 * prompt for an image and for a binary file, so those two assert the
 * composer's own contract and no reply.
 */
opencodeTest.describe('OpenCode attachments', () => {
  // The file name states a word the prompt never gives, so the name in the user
  // message can only come from the attachment.
  opencodeTest('accepts a text attachment and carries it through the turn', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
    void authenticatedOpencodeWorkspace
    await modelScript.queue({ text: 'The note is attached.' })
    await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'opencode-notes.txt' })
    await sendWithAttachment(page, modelScript.prompt('Read the attached note.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'opencode-notes.txt')
    await expect(assistantBubbles(page).filter({ hasText: 'The note is attached.' }).first()).toBeVisible()
  })

  opencodeTest('accepts an image attachment and sends it', async ({ authenticatedOpencodeWorkspace, page }) => {
    void authenticatedOpencodeWorkspace
    // No scripted reply: the shipped OpenCode ACP adapter answers the prompt
    // with "OpenCode service failure" when it carries an image, so the request
    // never reaches the model. Grok completes the same ACP blocks. The
    // composer's own contract is the pill, the send and the recorded message.
    await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'opencode-shot.png' })
    await sendWithAttachment(page, 'Describe the attached image.')

    await expectUserMessage(page, 'opencode-shot.png')
  })

  opencodeTest('accepts a PDF attachment and carries it through the turn', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
    void authenticatedOpencodeWorkspace
    await modelScript.queue({ text: 'The PDF is attached.' })
    await expectAttachmentOutcome(page, 'pdf', { supported: true, fileName: 'opencode-doc.pdf' })
    await sendWithAttachment(page, modelScript.prompt('Read the attached PDF.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectUserMessage(page, 'opencode-doc.pdf')
    await expect(assistantBubbles(page).filter({ hasText: 'The PDF is attached.' }).first()).toBeVisible()
  })

  opencodeTest('accepts a binary attachment and sends it', async ({ authenticatedOpencodeWorkspace, page }) => {
    void authenticatedOpencodeWorkspace
    // The adapter fails a binary attachment the same way it fails an image; see
    // the image test above. No scripted reply.
    await expectAttachmentOutcome(page, 'binary', { supported: true, fileName: 'opencode-blob.bin' })
    await sendWithAttachment(page, 'Inspect the attached file.')

    await expectUserMessage(page, 'opencode-blob.bin')
  })
})
