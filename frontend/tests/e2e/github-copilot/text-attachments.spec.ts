import { copilotTest } from '../copilot-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

copilotTest('delivers a text attachment to the model', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'text', 'copilot-notes.txt')
})
