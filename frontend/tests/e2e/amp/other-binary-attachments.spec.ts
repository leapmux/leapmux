import { ampTest } from '../amp-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

ampTest('refuses the attachment kind and excludes its actual bytes from the next native request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'binary', fileName: 'amp-refused.bin' })
})
