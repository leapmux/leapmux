import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { MIMO_AGENT, nativeContext } from './scenarios'

mimoTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: MIMO_AGENT, nativeContext, skipReason: MIMO_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
