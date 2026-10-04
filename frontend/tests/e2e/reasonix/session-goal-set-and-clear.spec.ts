import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixGoalLifecycle } from './goalScenario'

reasonixTest('sets and clears a native Reasonix goal before and after cancellation', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  await exerciseReasonixGoalLifecycle(context)
})
