import { DIRAC_E2E_SKIP_REASON, diracTest, expect } from './dirac-fixtures'
import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { diracRespondToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, waitForAgentIdle } from './helpers/ui'

diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

/**
 * 275 — Dirac attachments.
 *
 * Dirac takes text and image attachments (matrix). It takes no PDF, and the
 * matrix marks that cell false, so no test covers it here. Each send ends its
 * turn at `respond complete`.
 */
diracTest.describe('Dirac attachments', () => {
  diracTest('accepts a text attachment and carries it through the turn', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'dirac-notes.txt' })

    await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-text', 'complete', 'The note is attached.')] })
    await sendWithAttachment(page, modelScript.prompt('Read the attached note.'))
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'The note is attached.' }).first()).toBeVisible()
  })

  diracTest('accepts an image attachment and carries it through the turn', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'dirac-shot.png' })

    await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-image', 'complete', 'The image is attached.')] })
    await sendWithAttachment(page, modelScript.prompt('Read the attached image.'))
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'The image is attached.' }).first()).toBeVisible()
  })
})
