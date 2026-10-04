import { GEMINI_E2E_SKIP_REASON, geminiTest } from '../gemini-fixtures'
import { exerciseNativeCompactCommandLimit, nativeContext } from './scenarios'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest('preserves the model context when the unsupported native compact command reaches the model', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseNativeCompactCommandLimit(context)
})
