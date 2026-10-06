import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('refuses native binary input and keeps its actual bytes out of a clean model request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'binary', fileName: 'reasonix-refused.bin' })
})
