import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code attachments and context usage', () => {
  lettaTest('delivers image attachment bytes to the model', async ({ authenticatedVisionLettaWorkspace, page, modelScript }) => {
    void authenticatedVisionLettaWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'letta-shot.png', { protocol: 'openai-responses' })
  })
})
