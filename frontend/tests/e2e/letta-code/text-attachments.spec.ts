import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code attachments and context usage', () => {
  lettaTest('delivers text attachment bytes to the model', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'letta-notes.txt')
  })
})
