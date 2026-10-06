import { AgentGoalAction, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset } from '../helpers/ui'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixGoalLifecycle } from './goalScenario'

reasonixTest('session-goal-pause-and-resume: sets and clears a native Reasonix goal before and after cancellation', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  await exerciseReasonixGoalLifecycle(context)
})

reasonixTest('proves the native session-goal-pause-and-resume limit after a real sidebar operation', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  const relatedProof = () => exerciseRelatedTodo(context, { prepare: () => applyPermissionPreset(page, 'bypass') })
  await expectUnsupportedGoalActions(context, { actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME], relatedProof })
})
