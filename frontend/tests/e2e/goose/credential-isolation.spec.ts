import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

gooseTest('uses private provider configuration and the actual mock credential', async ({ authenticatedGooseWorkspace, page, modelScript, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  if (!environment)
    throw new Error('The credential scenario requires the suite isolated environment.')
  await exerciseCredentialIsolation({ page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId, provider: AgentProvider.GOOSE }, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!, environment.GOOSE_PATH_ROOT!],
    configurationFiles: [join(environment.GOOSE_PATH_ROOT!, 'config', 'config.yaml')],
    inlineConfiguration: [environment.OPENAI_BASE_URL!, environment.OPENAI_API_KEY!],
  })
})
