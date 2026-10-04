import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { exerciseModelError } from '../helpers/nativeModelError'

claudeTest('reports a native model error and accepts the next turn', async ({ authenticatedClaudeWorkspace, page, modelScript }) => {
  void authenticatedClaudeWorkspace
  await exerciseModelError({ page, modelScript, provider: AgentProvider.CLAUDE_CODE })
})
