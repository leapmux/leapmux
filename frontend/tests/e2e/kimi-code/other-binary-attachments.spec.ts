import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { kimiTest } from '../kimi-fixtures'

kimiTest('refuses the attachment kind and excludes its actual bytes from the next native request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'binary', fileName: 'kimi-code-refused.bin' })
})
