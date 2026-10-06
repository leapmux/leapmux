import { clineTest } from '../cline-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

clineTest.describe('Cline attachments', () => {
  clineTest('accepts an image attachment and carries it through the turn', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'image', 'cline-shot.png')
  })
})
