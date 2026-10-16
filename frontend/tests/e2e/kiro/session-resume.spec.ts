import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { KIRO_AGENT, nativeContext } from './scenarios'

kiroTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: KIRO_AGENT, nativeContext, skipReason: KIRO_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
