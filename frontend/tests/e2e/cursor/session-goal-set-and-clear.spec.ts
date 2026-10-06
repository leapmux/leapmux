import { AgentGoalAction } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { exerciseCursorRelatedTodo } from './scenarios'

cursorTest('proves the native session-goal-set-and-clear limit after a real sidebar operation', async ({ native }) => {
  await expectUnsupportedGoalActions(native, { actions: [AgentGoalAction.SET, AgentGoalAction.CLEAR], relatedProof: () => exerciseCursorRelatedTodo(native) })
})
