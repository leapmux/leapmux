import { claudeTest } from '../claude-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

claudeTest('refuses audio outside the model API input types and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'claude-code-refused-audio.wav' })
})
