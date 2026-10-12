import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

fastAgentTest.describe('Fast Agent attachments', () => {
  fastAgentTest('refuses another binary attachment before a model request', async ({ native }) => {
    await exerciseAttachmentRefusal(native, 'binary')
  })
})
