import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'

codexTest('preserves the previous prompt and answer in the next native request', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
  void authenticatedCodexWorkspace
  await exerciseConversationContext({ page, modelScript, provider: AgentProvider.CODEX })
})
