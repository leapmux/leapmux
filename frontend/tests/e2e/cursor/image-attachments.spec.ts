import { cursorTest } from '../cursor-fixtures'
import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'

cursorTest('delivers an image attachment to the model', async ({ native }) => {
  await exerciseImageAttachmentDelivery(native, 'cursor-shot.png', { protocol: 'openai-responses' })
})
