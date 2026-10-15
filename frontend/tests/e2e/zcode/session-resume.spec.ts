import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from '../zcode-fixtures'
import { nativeContext, ZCODE_AGENT } from './scenarios'

zcodeTest('reopens the native picker session and restores saved Worker messages', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: ZCODE_AGENT, nativeContext, skipReason: ZCODE_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
