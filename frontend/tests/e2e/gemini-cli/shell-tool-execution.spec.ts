import { geminiTest } from '../gemini-fixtures'
import { nativeContext } from './scenarios'
import { exerciseGeminiShellToolExecution } from './shellScenarios'

geminiTest('runs native shell output and failed commands', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseGeminiShellToolExecution(context)
})
