import { join } from 'node:path'
import { expect } from '@playwright/test'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { createTestDirectory } from '../helpers/runDirectory'
import { writeToolImage } from '../helpers/toolImages'
import { assistantBubbles, expectUserMessage, waitForAgentIdle } from '../helpers/ui'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest.describe('Kiro attachments', () => {
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
})
