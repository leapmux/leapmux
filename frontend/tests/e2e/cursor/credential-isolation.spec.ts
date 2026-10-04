import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

cursorTest('uses private provider configuration and the actual mock credential', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  if (!environment)
    throw new Error('The credential scenario requires the suite isolated environment.')
  await exerciseCredentialIsolation({ page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!, environment.CURSOR_CONFIG_DIR!],
    inlineConfiguration: [environment.CURSOR_API_ENDPOINT!, environment.CURSOR_AUTH_TOKEN!, environment.AGENT_CLI_CREDENTIAL_STORE!],
    configurationMarkers: ['memory'],
  })
})
