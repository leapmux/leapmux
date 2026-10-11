import { copilotTest } from '../copilot-fixtures'
import { exerciseAttachmentRefusal } from '../helpers/attachmentModelProbe'

copilotTest('keeps a refused PDF attachment out of the next request', async ({ native }) => {
  await exerciseAttachmentRefusal(native, { kind: 'pdf', fileName: 'copilot-doc.pdf' })
})
