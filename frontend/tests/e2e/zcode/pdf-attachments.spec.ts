// The installed native PDF attachment path is not integrated.
// See https://github.com/leapmux/leapmux/issues/499.
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('refuses native pdf input and keeps its actual bytes out of a clean model request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'pdf', fileName: 'zcode-refused.pdf' })
})
