import { diracTest } from '../dirac-fixtures'
import { exerciseImageAttachmentDelivery, expectAttachmentInRequest } from '../helpers/attachmentModelProbe'
import { diracAttachmentRequest } from './attachmentInput'

diracTest.describe('attachments for Dirac', () => {
  diracTest('accepts an image attachment and carries it through the turn', async ({ native }) => {
    await exerciseImageAttachmentDelivery(native, 'dirac-shot.png', {
      proof: (request, sourcePath) => expectAttachmentInRequest(native.page, diracAttachmentRequest(request), 'image', sourcePath),
    })
  })
})
