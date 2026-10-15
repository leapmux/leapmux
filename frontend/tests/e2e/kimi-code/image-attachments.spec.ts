import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('attachments for Kimi Code', () => {
  kimiTest('accepts an image attachment and carries it through the turn', async ({ native }) => {
    await exerciseImageAttachmentDelivery(native, 'kimi-shot.png')
  })
})
