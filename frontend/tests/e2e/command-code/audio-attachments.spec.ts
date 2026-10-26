import { commandCodeTest } from '../command-code-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

commandCodeTest('refuses audio outside native input types and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'command-code-refused-audio.wav' })
})
