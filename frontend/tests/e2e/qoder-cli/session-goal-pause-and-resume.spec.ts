import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { qoderTest } from '../qoder-fixtures'
import { exerciseNativeGoalCycle } from './goalScenarios'

qoderTest.describe('Qoder CLI effort and session goal', () => {
  qoderTest('sets, pauses, resumes, and clears a native session goal', async ({ authenticatedQoderWorkspace, page, modelScript }) => {
    void authenticatedQoderWorkspace
    await exerciseNativeGoalCycle({ page, modelScript, provider: AgentProvider.QODER })
  })
})
