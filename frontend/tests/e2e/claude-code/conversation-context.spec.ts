import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

claudeTest('preserves the previous prompt and answer in the next native request', async ({ authenticatedClaudeWorkspace, page, modelScript }) => {
  void authenticatedClaudeWorkspace
  await exerciseConversationContext({ page, modelScript, provider: AgentProvider.CLAUDE_CODE })
})
