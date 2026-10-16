import { clineTest } from '../cline-fixtures'
import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'

clineTest.describe('attachments for Cline', () => {
  clineTest('accepts an image attachment and carries it through the turn', async ({ native }) => {
    await exerciseImageAttachmentDelivery(native, 'cline-shot.png')
  })
})
