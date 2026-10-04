import { GEMINI_E2E_SKIP_REASON, geminiTest } from '../gemini-fixtures'
import { nativeContext } from './scenarios'
import { exerciseGeminiShellToolExecution } from './shellScenarios'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest('runs native shell output and failed commands', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseGeminiShellToolExecution(context)
})
