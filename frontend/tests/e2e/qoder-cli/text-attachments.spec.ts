import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI attachments and context usage', () => {
  qoderTest('delivers text attachment bytes to the model', async ({ authenticatedQoderWorkspace, page, modelScript }) => {
    void authenticatedQoderWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'text', 'qoder-notes.txt')
  })
})
