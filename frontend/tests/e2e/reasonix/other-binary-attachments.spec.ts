import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { reasonixTest } from '../reasonix-fixtures'
import { expectReasonixAttachmentBytes } from './attachmentScenario'

reasonixTest('delivers every binary byte to the native attachment that the current prompt addresses', async ({ native, authenticatedReasonixWorkspace }) => {
  await exerciseAttachmentDelivery(native, 'binary', 'reasonix-data.bin', {
    proof: (request, sourcePath) => { expectReasonixAttachmentBytes(request, sourcePath, authenticatedReasonixWorkspace.workingDir) },
  })
})
