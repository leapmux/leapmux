import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { relatedNativeProof } from './scenarios'

gooseTest('proves the native session-goal-pause-and-resume limit after a real sidebar operation', async ({ native }) => {
  await expectUnsupportedGoalActions(native, { actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME], relatedProof: () => relatedNativeProof(native) })
})
