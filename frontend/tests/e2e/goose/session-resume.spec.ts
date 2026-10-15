import { GOOSE_E2E_SKIP_REASON, gooseTest } from '../goose-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { GOOSE_AGENT, nativeContext } from './scenarios'

gooseTest('reopens the native picker session and restores saved Worker messages', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: GOOSE_AGENT, nativeContext, skipReason: GOOSE_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
