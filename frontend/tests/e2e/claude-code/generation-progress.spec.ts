import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'

claudeTest('advances the native model counter while each output segment remains held', async ({ authenticatedClaudeWorkspace, page, leapmuxServer, modelScript }) => {
  await exerciseGenerationProgress({ page, modelScript, leapmuxServer, provider: AgentProvider.CLAUDE_CODE, workspaceId: authenticatedClaudeWorkspace.workspaceId }, { supported: true, counter: 'tokens' })
})
