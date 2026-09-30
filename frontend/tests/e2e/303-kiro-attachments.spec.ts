import { join } from 'node:path'
import { exerciseAttachmentDelivery, expectNativeAttachmentProof, expectRefusedAttachmentsAbsent } from './helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from './helpers/attachments'
import { createTestDirectory } from './helpers/runDirectory'
import { writeToolImage } from './helpers/toolImages'
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
  kiroTest('accepts a text attachment and carries it through the turn', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'kiro-notes.txt')
  })

  kiroTest('accepts an image attachment and carries it through the turn', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    const directory = createTestDirectory('kiro-attachment-image-')
    const fileName = writeToolImage(directory, 'kiro-attachment')
    const imagePath = join(directory, fileName)
    await modelScript.queue({ text: 'Attachment received.' })
    await expectAttachmentOutcome(page, 'image', { supported: true, fileName, fixturePath: imagePath })
    await sendWithAttachment(page, modelScript.prompt('Inspect the attached file.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    await expectNativeAttachmentProof(page, status, 'image', imagePath, 'aws-event-stream')
    await expectUserMessage(page, fileName)
    await expect(assistantBubbles(page).filter({ hasText: 'Attachment received.' }).first()).toBeVisible()
  })

  kiroTest('accepts a PDF attachment and carries it through the turn', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'pdf', 'kiro-doc.pdf')
  })

  kiroTest('refuses a binary file', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected])
  })
})
