import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code attachments', () => {
  mimoTest('accepts a text attachment and carries it through the turn', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'mimo-notes.txt')
  })
})
