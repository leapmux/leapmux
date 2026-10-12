import { diracTest } from '../dirac-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

diracTest.describe('Dirac attachments', () => {
  diracTest('refuses another binary attachment that Dirac cannot read', async ({ native }) => {
    await exerciseAttachmentRefusal(native, 'binary')
  })
})
