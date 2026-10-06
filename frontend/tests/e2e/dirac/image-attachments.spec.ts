import { diracTest } from '../dirac-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

diracTest.describe('Dirac attachments', () => {
  diracTest('accepts an image attachment and carries it through the turn', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'image', 'dirac-shot.png')
  })
})
