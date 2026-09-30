import { DIRAC_E2E_SKIP_REASON, diracTest, expect } from './dirac-fixtures'
import { expectNativeAttachmentProof, expectRefusedAttachmentsAbsent } from './helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { diracRespondToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, waitForAgentIdle } from './helpers/ui'

diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

/**
 * 275 — Dirac attachments.
 *
 * Dirac takes text and image attachments. Its `respond complete` call ends
 * each turn. The model request must contain the file bytes.
 */
diracTest.describe('Dirac attachments', () => {
  diracTest('accepts a text attachment and carries it through the turn', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    const sourcePath = await expectAttachmentOutcome(page, 'text', { supported: true, fileName: 'dirac-notes.txt' })

    await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-text', 'complete', 'The note is attached.')] })
    await sendWithAttachment(page, modelScript.prompt('Read the attached note.'))
    const status = await modelScript.waitForSteps()
    await expectNativeAttachmentProof(page, status, 'text', sourcePath)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'The note is attached.' }).first()).toBeVisible()
  })

  diracTest('accepts an image attachment and carries it through the turn', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    const sourcePath = await expectAttachmentOutcome(page, 'image', { supported: true, fileName: 'dirac-shot.png' })

    await modelScript.queue({ toolCalls: [diracRespondToolCall('dirac-image', 'complete', 'The image is attached.')] })
    await sendWithAttachment(page, modelScript.prompt('Read the attached image.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectNativeAttachmentProof(page, status, 'image', sourcePath)
    await expect(assistantBubbles(page).filter({ hasText: 'The image is attached.' }).first()).toBeVisible()
  })

  diracTest('refuses a PDF attachment that Dirac cannot read', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    const rejected = await expectAttachmentOutcome(page, 'pdf', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected], {
      toolCalls: [diracRespondToolCall('dirac-clean-pdf', 'complete', 'The clean prompt ended.')],
    })
  })

  diracTest('refuses another binary attachment that Dirac cannot read', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected], {
      toolCalls: [diracRespondToolCall('dirac-clean-binary', 'complete', 'The clean prompt ended.')],
    })
  })
})
