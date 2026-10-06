import { diracTest } from '../dirac-fixtures'
import { expectRefusedAttachmentsAbsent } from '../helpers/attachmentModelProbe'
import { expectAttachmentOutcome } from '../helpers/attachments'
import { diracRespondToolCall } from '../helpers/providerToolCalls'

diracTest.describe('Dirac attachments', () => {
  diracTest('refuses another binary attachment that Dirac cannot read', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    const rejected = await expectAttachmentOutcome(page, 'binary', { supported: false })
    await expectRefusedAttachmentsAbsent(page, modelScript, [rejected], {
      toolCalls: [diracRespondToolCall('dirac-clean-binary', 'complete', 'The clean prompt ended.')],
    })
  })
})
