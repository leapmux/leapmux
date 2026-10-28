import { cursorTest } from '../cursor-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

cursorTest('refuses audio that the native protocol cannot forward and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'cursor-refused-audio.wav' })
})
