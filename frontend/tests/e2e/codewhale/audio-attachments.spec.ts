import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

codewhaleTest('refuses audio outside native input types and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'codewhale-refused-audio.wav' })
})
