import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { kiloTest } from '../kilo-fixtures'

kiloTest('uses private provider configuration and the actual mock credential', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  if (!environment)
    throw new Error('The credential scenario requires the suite isolated environment.')
  await exerciseCredentialIsolation({ page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!],
    inlineConfiguration: [environment.KILO_CONFIG_CONTENT!],
    configurationMarkers: ['leapmux-e2e'],
  })
})
