import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

claudeTest('closes the native process and its real tool through the UI', async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
  await exerciseCloseAgent({ page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId })
})
