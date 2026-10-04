import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('uses private provider configuration and the actual mock credential', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  if (!environment)
    throw new Error('The credential scenario requires the suite isolated environment.')
  await exerciseCredentialIsolation({ page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!, environment.ZCODE_STORAGE_DIR!],
    configurationFiles: [environment.ZCODE_PERSONAL_PROVIDER_CONFIG_FILE!],
  })
})
