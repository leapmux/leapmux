import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('attachments for MiMo Code', () => {
  mimoTest('accepts an image attachment and carries it through the turn', async ({ native }) => {
    await exerciseImageAttachmentDelivery(native, 'mimo-shot.png')
  })
})
