import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('refuses native pdf input and keeps its actual bytes out of a clean model request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'pdf', fileName: 'reasonix-refused.pdf' })
})
