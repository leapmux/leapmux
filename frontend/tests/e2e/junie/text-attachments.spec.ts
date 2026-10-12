import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { junieTest } from '../junie-fixtures'

junieTest.describe('Junie attachments and context usage', () => {
  junieTest('delivers text attachment bytes to the model', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'jnote.txt')
  })
})
