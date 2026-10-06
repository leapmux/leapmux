import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

codewhaleTest('loads private native configuration and calls only the suite mock', async ({ page, modelScript, leapmuxServer, authenticatedCodewhaleWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCodewhaleWorkspace.workspaceId, provider: AgentProvider.CODEWHALE }
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(context, { configurationFiles: [join(environment.CODEWHALE_HOME!, 'config.toml')], privateDirectories: [leapmuxServer.agentEnv.CODEWHALE_HOME!], expectedCredential: environment.LEAPMUX_E2E_MODEL_API_KEY! })
})
