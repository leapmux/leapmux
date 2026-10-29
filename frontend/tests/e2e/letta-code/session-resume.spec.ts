import { exerciseSessionResume } from '../helpers/nativeLifecycle'
import { exerciseWorkerRestart } from '../helpers/nativeWorkerRestart'
import { LETTA_E2E_SKIP_REASON, lettaTest } from '../letta-fixtures'
import { createProviderProcessTest } from '../provider-fixture-factory'
import { LETTA_TITLE_RULE } from './housekeeping'
import { LETTA_AGENT, nativeContext } from './scenarios'

lettaTest('reopens a completed picker session and restores its saved Worker rows', async ({ native }) => {
  await exerciseSessionResume(native)
})

const restartTest = createProviderProcessTest({
  agent: LETTA_AGENT,
  nativeContext,
  skipReason: LETTA_E2E_SKIP_REASON,
  modelRules: [LETTA_TITLE_RULE],
})

for (const restart of ['worker', 'hub-and-worker'] as const) {
  restartTest(`retains native context and transcript after a ${restart} restart`, async ({ native, separateHubWorker }) => {
    await exerciseWorkerRestart(native, separateHubWorker, restart)
  })
}
