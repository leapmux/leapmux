import { gooseTest } from '../goose-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

gooseTest('refuses audio that the native protocol drops and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'goose-refused-audio.wav' })
})
