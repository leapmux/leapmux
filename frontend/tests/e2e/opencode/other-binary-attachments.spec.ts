import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('refuses a binary attachment before it enters the queue', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'binary', fileName: 'opencode-blob.bin' })
})
