import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest } from '../codebuddy-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { CODEBUDDY_AGENT, nativeContext } from './scenarios'

codebuddyTest('reopens a completed picker session and restores its saved Worker rows', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: CODEBUDDY_AGENT, nativeContext, skipReason: CODEBUDDY_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
