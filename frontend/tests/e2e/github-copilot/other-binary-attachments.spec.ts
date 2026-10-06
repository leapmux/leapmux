import { copilotTest } from '../copilot-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

copilotTest('keeps a refused binary attachment out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'binary', fileName: 'copilot-blob.bin' })
})
