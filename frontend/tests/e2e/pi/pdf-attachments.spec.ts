import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { piTest } from '../pi-fixtures'

piTest('refuses native pdf input and keeps its actual bytes out of a clean model request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'pdf', fileName: 'pi-refused.pdf' })
})
