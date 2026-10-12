import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { museTest } from '../muse-fixtures'

museTest('refuses native binary input and keeps its actual bytes out of a clean model request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'binary', fileName: 'muse-refused.bin' })
})
