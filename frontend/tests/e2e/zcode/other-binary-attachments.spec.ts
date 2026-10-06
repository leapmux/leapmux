import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('refuses native binary input and keeps its actual bytes out of a clean model request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'binary', fileName: 'zcode-refused.bin' })
})
