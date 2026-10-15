import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest } from '../codewhale-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { CODEWHALE_AGENT, nativeContext } from './scenarios'

codewhaleTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: CODEWHALE_AGENT, nativeContext, skipReason: CODEWHALE_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
