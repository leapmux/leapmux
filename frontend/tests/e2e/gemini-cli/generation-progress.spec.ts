import { geminiTest } from '../gemini-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { nativeContext } from './scenarios'

geminiTest('updates the native generation counter before a held turn completes', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens' })
})
