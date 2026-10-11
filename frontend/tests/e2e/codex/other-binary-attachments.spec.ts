import { codexTest } from '../codex-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

codexTest('refuses binary attachments and excludes their actual bytes from the next native request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'binary', fileName: 'codex-refused.bin' })
})
