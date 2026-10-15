import { DEEPSEEK_HARNESS_E2E_SKIP_REASON, deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { DEEPSEEK_HARNESS_AGENT, nativeContext } from './scenarios'

deepseekHarnessTest('reopens a native session and restores its stored Worker transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: DEEPSEEK_HARNESS_AGENT, nativeContext, skipReason: DEEPSEEK_HARNESS_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
