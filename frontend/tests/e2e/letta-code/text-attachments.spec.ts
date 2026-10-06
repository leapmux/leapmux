import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code attachments and context usage', () => {
  lettaTest('delivers text attachment bytes to the model', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'letta-notes.txt')
  })
})
