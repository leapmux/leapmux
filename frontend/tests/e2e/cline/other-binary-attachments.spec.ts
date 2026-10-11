import { clineTest } from '../cline-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

clineTest('refuses the attachment kind and excludes its actual bytes from the next native request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'binary', fileName: 'cline-refused.bin' })
})
