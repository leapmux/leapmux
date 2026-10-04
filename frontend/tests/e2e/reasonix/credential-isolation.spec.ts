import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('uses private provider configuration and the actual mock credential', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  if (!environment)
    throw new Error('The credential scenario requires the suite isolated environment.')
  await exerciseCredentialIsolation({ page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!, environment.REASONIX_HOME!],
    configurationFiles: [join(environment.REASONIX_HOME!, 'config.toml')],
    inlineConfiguration: [environment.LEAPMUX_E2E_MODEL_API_KEY!],
    configurationMarkers: ['LEAPMUX_E2E_MODEL_API_KEY'],
  })
})
