import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { kimiTest } from '../kimi-fixtures'

kimiTest('refuses audio that the native input and media reader cannot carry and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'kimi-refused-audio.wav' })
})
