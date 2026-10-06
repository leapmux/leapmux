import { diracTest } from '../dirac-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

diracTest.describe('Dirac attachments', () => {
  diracTest('refuses a PDF attachment that Dirac cannot read', async ({ native }) => {
    await exerciseAttachmentRefusal(native, 'pdf')
  })
})
