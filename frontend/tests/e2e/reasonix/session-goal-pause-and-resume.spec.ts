import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { applyPermissionPreset } from '../helpers/ui'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixGoalLifecycle } from './goalScenario'

reasonixTest('session-goal-pause-and-resume: sets and clears a native Reasonix goal before and after cancellation', async ({ native }) => {
  await exerciseReasonixGoalLifecycle(native)
})

reasonixTest('proves the native session-goal-pause-and-resume limit after a real sidebar operation', async ({ native }) => {
  const relatedProof = () => exerciseRelatedTodo(native, { prepare: () => applyPermissionPreset(native.page, 'bypass') })
  await expectUnsupportedGoalActions(native, { actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME], relatedProof })
})
