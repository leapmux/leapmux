import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI attachments and context usage', () => {
  qoderTest('delivers text attachment bytes to the model', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'qoder-notes.txt')
  })
})
