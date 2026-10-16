import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from '../opencode-fixtures'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { nativeContext, OPENCODE_AGENT } from './scenarios'

opencodeTest('reopens the native picker session and restores saved Worker messages', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: OPENCODE_AGENT, nativeContext, skipReason: OPENCODE_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
