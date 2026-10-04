import { AgentGoalAction, AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectUnsupportedGoalActions } from '../helpers/unsupportedConfiguration'

claudeTest('refuses unsupported native goal pause and resume actions through the Worker', async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
  const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId }
  await expectUnsupportedGoalActions(context, {
    actions: [AgentGoalAction.PAUSE, AgentGoalAction.RESUME],
    relatedProof: () => exerciseShellToolExecution(context),
  })
})
