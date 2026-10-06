import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('proves the native session-goal-set-and-clear limit after a real sidebar operation', async ({ native }) => {
  await expectUnsupportedGoalActions(native, { actions: [AgentGoalAction.SET, AgentGoalAction.CLEAR], relatedProof: () => exerciseRelatedTodo(native) })
})
