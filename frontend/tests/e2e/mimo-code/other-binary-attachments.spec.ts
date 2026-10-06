import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code attachments', () => {
  mimoTest('refuses a binary file', async ({ native }) => {
    await exerciseAttachmentRefusal(native, 'binary')
  })
})
