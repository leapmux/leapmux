import { GEMINI_E2E_SKIP_REASON, geminiTest } from '../gemini-fixtures'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { exerciseGeminiResumeWithEvidence } from './resumeEvidence'
import { GEMINI_AGENT, nativeContext } from './scenarios'

geminiTest('reopens the native session and restores the saved transcript', async ({ native }, testInfo) => {
  await exerciseGeminiResumeWithEvidence(native, testInfo)
})

const restartTest = createProviderProcessTest({ agent: GEMINI_AGENT, nativeContext, skipReason: GEMINI_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
