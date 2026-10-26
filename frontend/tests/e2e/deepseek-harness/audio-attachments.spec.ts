import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

deepseekHarnessTest('refuses audio outside native input types and keeps its bytes out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'audio', fileName: 'deepseek-harness-refused-audio.wav' })
})
