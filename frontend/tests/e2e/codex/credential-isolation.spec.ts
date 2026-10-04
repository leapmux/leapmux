import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexTest } from '../codex-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

codexTest('uses private provider configuration and the actual mock credential', async ({ authenticatedCodexWorkspace, page, modelScript, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  if (!environment)
    throw new Error('The credential scenario requires the suite isolated environment.')
  await exerciseCredentialIsolation({ page, modelScript, leapmuxServer, workspaceId: authenticatedCodexWorkspace.workspaceId, provider: AgentProvider.CODEX }, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!, environment.CODEX_HOME!],
    configurationFiles: [join(environment.CODEX_HOME!, 'config.toml')],
    inlineConfiguration: [environment.LEAPMUX_E2E_MODEL_API_KEY!],
    configurationMarkers: ['LEAPMUX_E2E_MODEL_API_KEY'],
  })
})
