import { expect } from '@playwright/test'
import { geminiTest } from '../gemini-fixtures'
import { expectNativeResumeContext } from '../helpers/nativeResume'
import { nativeModelConversationTurns } from '../helpers/nativeScenario'
import { exerciseGeminiResumeWithEvidence } from './resumeEvidence'
import { nativeContext } from './scenarios'

geminiTest('loads the original native context through the stored-session picker', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }, testInfo) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const resumed = await exerciseGeminiResumeWithEvidence(context, testInfo)
  expect(resumed.request.protocol).toBe('google-generative-language')
  expect(JSON.stringify(resumed.request.body)).toContain('RESUMEPROMPT')
  expect(JSON.stringify(resumed.request.body)).toContain('RESUMEANSWER')
  // Gemini states the history as `contents`: the original prompt in a user part, its answer in a later model part.
  expectNativeResumeContext(nativeModelConversationTurns(resumed.request), resumed)
})
