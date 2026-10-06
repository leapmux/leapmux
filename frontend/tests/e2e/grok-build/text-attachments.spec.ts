import { grokTest } from '../grok-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

grokTest.describe('Grok Build attachments', () => {
  grokTest('accepts a text attachment and carries it through the turn', async ({ native }) => {
    await exerciseAttachmentDelivery(native, 'text', 'grok-notes.txt')
  })
})
