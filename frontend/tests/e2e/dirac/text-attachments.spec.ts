import { DIRAC_E2E_SKIP_REASON, diracTest, expect } from '../dirac-fixtures'
import { expectNativeAttachmentProof } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome, sendWithAttachment } from '../helpers/attachments'
import { diracRespondToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, waitForAgentIdle } from '../helpers/ui'

diracTest.describe('Dirac attachments', () => {
  diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

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
})
