import { grokTest } from '../grok-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

grokTest('refuses unsupported native audio and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'grok-refused-audio.wav' })
})
