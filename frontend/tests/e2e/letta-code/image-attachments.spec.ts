import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest.describe('Letta Code attachments and context usage', () => {
  lettaTest('delivers image attachment bytes to the model', async ({ page, modelScript, leapmuxServer, authenticatedVisionLettaWorkspace }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedVisionLettaWorkspace.workspaceId })
    await exerciseAttachmentDelivery(context, 'image', 'letta-shot.png', { protocol: 'openai-responses' })
  })
})
