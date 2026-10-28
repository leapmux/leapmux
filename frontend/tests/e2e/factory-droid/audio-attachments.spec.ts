import { droidTest } from '../droid-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

droidTest('refuses audio outside native input types and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'factory-droid-refused-audio.wav' })
})
