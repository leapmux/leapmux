import { cursorTest } from '../cursor-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

cursorTest('other-binary-attachments: keeps refused PDF and binary attachments out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'pdf', fileName: 'cursor-doc.pdf' }, { kind: 'binary', fileName: 'cursor-blob.bin' })
})
