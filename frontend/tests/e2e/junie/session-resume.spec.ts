import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { JUNIE_E2E_SKIP_REASON, junieTest } from '../junie-fixtures'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { JUNIE_HOUSEKEEPING_RULES } from './housekeeping'
import { JUNIE_AGENT, nativeContext } from './scenarios'

junieTest('reopens a completed picker session and restores its saved Worker rows', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({ agent: JUNIE_AGENT, nativeContext, skipReason: JUNIE_E2E_SKIP_REASON, modelRules: JUNIE_HOUSEKEEPING_RULES })

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
