import { geminiTest } from '../gemini-fixtures'
import { exerciseBypassPermissions } from '../helpers/nativeBypassPermissions'
import { nativeContext } from './scenarios'

geminiTest('runs native tools without prompts after the bypass shortcut', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseBypassPermissions(context)
})
