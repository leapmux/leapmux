import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { KILO_E2E_SKIP_REASON, kiloTest } from '../kilo-fixtures'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { KILO_AGENT, nativeContext } from './scenarios'

kiloTest('reopens the native picker session and restores saved Worker messages', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: KILO_AGENT, nativeContext, skipReason: KILO_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
