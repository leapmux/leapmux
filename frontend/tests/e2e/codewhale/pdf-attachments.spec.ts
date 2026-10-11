import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

codewhaleTest('refuses the attachment kind and excludes its actual bytes from the next native request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'pdf', fileName: 'codewhale-refused.pdf' })
})
