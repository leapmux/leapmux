import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { QODER_E2E_SKIP_REASON, qoderTest } from '../qoder-fixtures'
import { exerciseNativeGoalCycle } from './goalScenarios'

qoderTest.describe('Qoder CLI effort and session goal', () => {
  qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

  qoderTest('sets, pauses, resumes, and clears a native session goal', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await exerciseNativeGoalCycle({ page, modelScript, provider: AgentProvider.QODER })
  })
})
