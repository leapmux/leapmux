import { AgentGoalAction, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseRelatedTodo } from '../helpers/relatedTodoProof'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('proves the native session-goal-pause-and-resume limit after a real sidebar operation', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
  const relatedProof = () => exerciseRelatedTodo(context)
  await expectUnsupportedGoalActions(context, { actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME], relatedProof })
})
