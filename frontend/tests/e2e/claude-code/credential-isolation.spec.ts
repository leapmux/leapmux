import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { claudeTest } from '../claude-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

claudeTest('uses private provider configuration and the actual mock credential', async ({ authenticatedClaudeWorkspace, page, modelScript, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  if (!environment)
    throw new Error('The credential scenario requires the suite isolated environment.')
  await exerciseCredentialIsolation({ page, modelScript, leapmuxServer, workspaceId: authenticatedClaudeWorkspace.workspaceId, provider: AgentProvider.CLAUDE_CODE }, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!, environment.CLAUDE_CONFIG_DIR!],
    inlineConfiguration: [environment.ANTHROPIC_BASE_URL!, environment.ANTHROPIC_API_KEY!],
  })
})
