import { codexTest } from '../codex-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

codexTest('refuses a PDF and excludes its actual bytes from the next native request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'pdf', fileName: 'codex-refused.pdf' })
})
