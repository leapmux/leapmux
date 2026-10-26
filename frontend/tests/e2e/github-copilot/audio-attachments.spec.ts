import { copilotTest } from '../copilot-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

copilotTest('refuses audio outside native input types and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'github-copilot-refused-audio.wav' })
})
