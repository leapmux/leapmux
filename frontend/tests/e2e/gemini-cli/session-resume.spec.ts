import { geminiTest } from '../gemini-fixtures'
import { exerciseGeminiResumeWithEvidence } from './resumeEvidence'

geminiTest('reopens the native session and restores the saved transcript', async ({ native }, testInfo) => {
  await exerciseGeminiResumeWithEvidence(native, testInfo)
})
