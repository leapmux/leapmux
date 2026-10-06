import { cursorTest } from '../cursor-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'

cursorTest('delivers an image attachment to the model', async ({ native }) => {
  await exerciseAttachmentDelivery(native, 'image', 'cursor-shot.png', { protocol: 'openai-responses' })
})
