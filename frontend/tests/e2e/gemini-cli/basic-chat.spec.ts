import { geminiTest } from '../gemini-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { nativeContext } from './scenarios'

geminiTest('completes a native conversation and preserves its answer after reload', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseBasicChat(context)
})
