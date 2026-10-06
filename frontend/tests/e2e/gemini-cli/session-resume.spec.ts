import { geminiTest } from '../gemini-fixtures'
import { exerciseGeminiResumeWithEvidence } from './resumeEvidence'
import { nativeContext } from './scenarios'

geminiTest('reopens the native session and restores the saved transcript', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }, testInfo) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseGeminiResumeWithEvidence(context, testInfo)
})
