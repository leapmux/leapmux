import { exerciseImageAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest.describe('attachments and context usage for Letta Code', () => {
  lettaTest('delivers image attachment bytes to the model', async ({ page, modelScript, leapmuxServer, authenticatedVisionLettaWorkspace }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedVisionLettaWorkspace.workspaceId })
    await exerciseImageAttachmentDelivery(context, 'letta-shot.png', { protocol: 'openai-responses' })
  })
})
