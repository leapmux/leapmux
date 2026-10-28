import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { kiroTest } from '../kiro-fixtures'

kiroTest('refuses audio outside the native document formats and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'kiro-refused-audio.wav' })
})
