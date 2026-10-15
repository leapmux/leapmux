import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { PI_E2E_SKIP_REASON, piTest } from '../pi-fixtures'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { nativeContext, PI_AGENT } from './scenarios'

piTest('reopens the native picker session and restores saved Worker messages', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: PI_AGENT, nativeContext, skipReason: PI_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
