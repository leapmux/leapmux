import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('Kimi Code attachments', () => {
  kimiTest('accepts an image attachment and carries it through the turn', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'image', 'kimi-shot.png')
  })
})
