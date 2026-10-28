import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { lettaTest } from '../letta-fixtures'

lettaTest('refuses audio outside native input types and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'letta-code-refused-audio.wav' })
})
