import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { copilotTest } from '../copilot-fixtures'
import { MOCK_COPILOT_GITHUB_TOKEN } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

copilotTest('uses private provider configuration and the actual mock credential', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  if (!environment)
    throw new Error('The credential scenario requires the suite isolated environment.')
  await exerciseCredentialIsolation({ page, modelScript, leapmuxServer, workspaceId: authenticatedCopilotWorkspace.workspaceId, provider: AgentProvider.GITHUB_COPILOT }, {
    expectedCredential: MOCK_COPILOT_GITHUB_TOKEN,
    privateDirectories: [environment.HOME!, environment.COPILOT_HOME!],
    inlineConfiguration: [environment.COPILOT_API_URL!, environment.COPILOT_GITHUB_TOKEN!],
  })
})
