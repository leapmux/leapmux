import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { MUSE_E2E_SKIP_REASON, museTest } from '../muse-fixtures'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { MUSE_AGENT, nativeContext } from './scenarios'

museTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: MUSE_AGENT, nativeContext, skipReason: MUSE_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
