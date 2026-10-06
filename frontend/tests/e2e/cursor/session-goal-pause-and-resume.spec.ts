import { AgentGoalAction, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { exerciseCursorRelatedTodo } from './scenarios'

cursorTest('proves the native session-goal-pause-and-resume limit after a real sidebar operation', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  const relatedProof = () => exerciseCursorRelatedTodo(context)
  await expectUnsupportedGoalActions(context, { actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME], relatedProof })
})
