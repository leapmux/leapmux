import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { junieTest } from '../junie-fixtures'

junieTest('refuses unsupported native audio and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'junie-refused-audio.wav' })
})
