import { geminiTest } from '../gemini-fixtures'
import { exerciseNativeCompactCommandLimit, nativeContext } from './scenarios'

geminiTest('preserves the model context when the unsupported native compact command reaches the model', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseNativeCompactCommandLimit(context)
})
