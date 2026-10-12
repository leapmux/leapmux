import { expect } from '@playwright/test'
import { geminiTest } from '../gemini-fixtures'
import { expectNativeResumeContext } from '../helpers/nativeResume'
import { nativeModelConversationTurns } from '../helpers/nativeScenario'
import { exerciseGeminiResumeWithEvidence } from './resumeEvidence'

geminiTest('loads the original native context through the stored-session picker', async ({ native }, testInfo) => {
  const resumed = await exerciseGeminiResumeWithEvidence(native, testInfo)
  expect(resumed.request.protocol).toBe('google-generative-language')
  expect(JSON.stringify(resumed.request.body)).toContain('RESUMEPROMPT')
  expect(JSON.stringify(resumed.request.body)).toContain('RESUMEANSWER')
  // Gemini states the history as `contents`: the original prompt in a user part, its answer in a later model part.
  expectNativeResumeContext(nativeModelConversationTurns(resumed.request), resumed)
})
