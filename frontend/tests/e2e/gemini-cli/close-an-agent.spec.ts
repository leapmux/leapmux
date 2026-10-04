import { GEMINI_E2E_SKIP_REASON, geminiTest } from '../gemini-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { nativeContext } from './scenarios'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest('closes the agent tab and waits for its owned native process', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseCloseAgent(context)
})
