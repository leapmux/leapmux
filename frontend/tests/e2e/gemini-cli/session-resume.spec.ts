import { GEMINI_E2E_SKIP_REASON, geminiTest } from '../gemini-fixtures'
import { exerciseGeminiResumeWithEvidence } from './resumeEvidence'
import { nativeContext } from './scenarios'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest('reopens the native session and restores the saved transcript', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }, testInfo) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await exerciseGeminiResumeWithEvidence(context, testInfo)
})
