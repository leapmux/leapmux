import { gooseTest } from '../goose-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

gooseTest('keeps refused PDF and binary attachments out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'pdf', fileName: 'goose-doc.pdf' }, { kind: 'binary', fileName: 'goose-blob.bin' })
})
