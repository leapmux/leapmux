import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { kiloTest } from '../kilo-fixtures'

kiloTest('keeps a refused binary attachment out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'binary', fileName: 'kilo-blob.bin' })
})
