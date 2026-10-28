import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('refuses audio that the native protocol drops and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'reasonix-refused-audio.wav' })
})
