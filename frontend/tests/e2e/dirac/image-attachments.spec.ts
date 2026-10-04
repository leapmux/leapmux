import { DIRAC_E2E_SKIP_REASON, diracTest, expect } from '../dirac-fixtures'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, waitForAgentIdle } from '../helpers/ui'

diracTest.describe('Dirac attachments', () => {
  diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

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
})
