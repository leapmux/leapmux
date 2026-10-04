import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'
import { expectMissingOptionGroup } from '../helpers/unsupportedConfiguration'

claudeTest('keeps the native catalog and restored UI free of the unsupported swarm-mode axis', async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
  const context = { page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId }
  await expectMissingOptionGroup(context, { groupId: 'swarmMode', relatedProof: () => exerciseConversationContext(context) })
})
