import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'
import { piTest } from '../pi-fixtures'

piTest('refuses audio outside native input types and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'pi-refused-audio.wav' })
})
