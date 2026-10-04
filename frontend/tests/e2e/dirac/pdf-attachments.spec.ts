import { DIRAC_E2E_SKIP_REASON, diracTest } from '../dirac-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { diracRespondToolCall } from '../helpers/providerToolCalls'

diracTest.describe('Dirac attachments', () => {
  diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

  diracTest('refuses a PDF attachment that Dirac cannot read', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    const rejected = await expectAttachmentOutcome(page, 'pdf', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected], {
      toolCalls: [diracRespondToolCall('dirac-clean-pdf', 'complete', 'The clean prompt ended.')],
    })
  })
})
