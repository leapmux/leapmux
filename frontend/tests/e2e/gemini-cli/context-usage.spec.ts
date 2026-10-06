import { geminiTest } from '../gemini-fixtures'
import { exerciseContextUsage } from '../helpers/contextUsage'
import { nativeContext } from './scenarios'

geminiTest('uses the last native request counts through the context surface', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseContextUsage(context.page, context.modelScript)
})
