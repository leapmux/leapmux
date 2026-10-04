import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { piTest } from '../pi-fixtures'

piTest('uses private provider configuration and the actual mock credential', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  if (!environment)
    throw new Error('The credential scenario requires the suite isolated environment.')
  await exerciseCredentialIsolation({ page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!, environment.PI_CODING_AGENT_DIR!],
    configurationFiles: [join(environment.PI_CODING_AGENT_DIR!, 'models.json'), join(environment.PI_CODING_AGENT_DIR!, 'settings.json'), join(environment.PI_CODING_AGENT_DIR!, 'mcp.json')],
  })
})
