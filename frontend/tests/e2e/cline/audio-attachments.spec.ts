import { clineTest } from '../cline-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

clineTest('refuses audio outside native input types and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'cline-refused-audio.wav' })
})
