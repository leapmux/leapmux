import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'
import { nativeContext, QWEN_AGENT } from './scenarios'

qwenTest('reopens the native picker handle and restores the saved transcript', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: QWEN_AGENT, nativeContext, skipReason: QWEN_E2E_SKIP_REASON })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
