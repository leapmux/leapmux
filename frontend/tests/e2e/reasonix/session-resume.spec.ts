import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from '../reasonix-fixtures'
import { nativeContext, REASONIX_AGENT } from './scenarios'

reasonixTest('reopens the native picker session and restores saved Worker messages', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: REASONIX_AGENT, nativeContext, skipReason: REASONIX_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
