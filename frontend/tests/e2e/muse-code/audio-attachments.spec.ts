import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { museTest } from '../muse-fixtures'

museTest('refuses audio outside native input types and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'muse-code-refused-audio.wav' })
})
