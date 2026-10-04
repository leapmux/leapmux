import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('uses private provider configuration and the actual mock credential', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  if (!environment)
    throw new Error('The credential scenario requires the suite isolated environment.')
  await exerciseCredentialIsolation({ page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!],
    inlineConfiguration: [environment.OPENCODE_CONFIG_CONTENT!],
    configurationMarkers: ['leapmux-e2e'],
  })
})
