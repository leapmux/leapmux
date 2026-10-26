import { ampTest } from '../amp-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

ampTest('refuses audio outside native input types and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'amp-refused-audio.wav' })
})
