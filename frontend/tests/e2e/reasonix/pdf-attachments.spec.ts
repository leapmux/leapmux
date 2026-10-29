import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { reasonixTest } from '../reasonix-fixtures'
import { expectReasonixAttachmentBytes } from './attachmentScenario'

reasonixTest('delivers every PDF byte to the native attachment that the current prompt addresses', async ({ native, authenticatedReasonixWorkspace }) => {
  await exerciseAttachmentDelivery(native, 'pdf', 'reasonix-document.pdf', {
    proof: (request, sourcePath) => { expectReasonixAttachmentBytes(request, sourcePath, authenticatedReasonixWorkspace.workingDir) },
  })
})
