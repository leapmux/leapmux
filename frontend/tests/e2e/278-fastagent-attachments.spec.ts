import { expect, FAST_AGENT_E2E_SKIP_REASON, fastAgentTest } from './fastagent-fixtures'
import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { assistantBubbles, waitForAgentIdle } from './helpers/ui'

fastAgentTest.skip(!!FAST_AGENT_E2E_SKIP_REASON, FAST_AGENT_E2E_SKIP_REASON || '')

/**
 * 278 — Fast Agent attachments.
 *
 * Fast Agent takes text, image and PDF attachments (matrix; the PDF rides its
 * `attach_media` staging, note 25). The composer accepts each kind and the
 * turn carries it.
 */
fastAgentTest.describe('Fast Agent attachments', () => {
  fastAgentTest('accepts a text attachment and carries it through the turn', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'fa-notes.txt' })

    await modelScript.queue({ text: 'The note is attached.' })
    await sendWithAttachment(page, modelScript.prompt('Read the attached note.'))
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'The note is attached.' }).first()).toBeVisible()
  })

  fastAgentTest('accepts an image attachment and carries it through the turn', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'fa-shot.png' })

    await modelScript.queue({ text: 'The image is attached.' })
    await sendWithAttachment(page, modelScript.prompt('Read the attached image.'))
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'The image is attached.' }).first()).toBeVisible()
  })

  fastAgentTest('accepts a PDF attachment and carries it through the turn', async ({ authenticatedFastAgentWorkspace, page, modelScript }) => {
    void authenticatedFastAgentWorkspace
    await expectAttachmentOutcome(page, 'pdf', { supported: true, fileName: 'fa-doc.pdf' })

    await modelScript.queue({ text: 'The document is attached.' })
    await sendWithAttachment(page, modelScript.prompt('Read the attached document.'))
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'The document is attached.' }).first()).toBeVisible()
  })
})
