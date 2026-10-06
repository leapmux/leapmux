import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixGoalLifecycle } from './goalScenario'
import { relatedNativeProof } from './scenarios'

reasonixTest('session-goal-pause-and-resume: sets and clears a native Reasonix goal before and after cancellation', async ({ native }) => {
  await exerciseReasonixGoalLifecycle(native)
})

reasonixTest('proves the native session-goal-pause-and-resume limit after a real sidebar operation', async ({ native }) => {
  await expectUnsupportedGoalActions(native, { actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME], relatedProof: () => relatedNativeProof(native) })
})
