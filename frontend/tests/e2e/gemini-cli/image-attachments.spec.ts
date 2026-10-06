import { geminiTest } from '../gemini-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { nativeContext } from './scenarios'

geminiTest('delivers the actual image attachment bytes through the native model request', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseAttachmentDelivery(context.page, context.modelScript, 'image', 'gemini-native.png', { protocol: 'google-generative-language' })
})
