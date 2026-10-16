import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { KIMI_AGENT, nativeContext } from './scenarios'

kimiTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: KIMI_AGENT, nativeContext, skipReason: KIMI_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
