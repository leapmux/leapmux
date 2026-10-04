import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI attachments and context usage', () => {
  qoderTest('delivers image attachment bytes to the model', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'qoder-shot.png')
  })
})
