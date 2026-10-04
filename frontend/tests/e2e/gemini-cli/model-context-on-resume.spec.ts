import { expect } from '@playwright/test'
import { GEMINI_E2E_SKIP_REASON, geminiTest } from '../gemini-fixtures'
import { exerciseGeminiResumeWithEvidence } from './resumeEvidence'
import { nativeContext } from './scenarios'

geminiTest.skip(!!GEMINI_E2E_SKIP_REASON, GEMINI_E2E_SKIP_REASON || '')

geminiTest('loads the original native context through the stored-session picker', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }, testInfo) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const request = await exerciseGeminiResumeWithEvidence(context, testInfo)
  expect(request.protocol).toBe('google-generative-language')
  expect(JSON.stringify(request.body)).toContain('RESUMEPROMPT')
  expect(JSON.stringify(request.body)).toContain('RESUMEANSWER')
})
