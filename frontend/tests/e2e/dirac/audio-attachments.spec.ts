import { diracTest } from '../dirac-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

diracTest('refuses audio that the native parser cannot carry and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'dirac-refused-audio.wav' })
})
