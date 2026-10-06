import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

fastAgentTest.describe('Fast Agent attachments', () => {
  fastAgentTest('delivers text attachment bytes to the model', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'fa-notes.txt')
  })
})
