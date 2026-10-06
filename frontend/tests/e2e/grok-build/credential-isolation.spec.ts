import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { grokTest } from '../grok-fixtures'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

grokTest('loads private native configuration and calls only the suite mock', async ({ page, modelScript, leapmuxServer, authenticatedGrokWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGrokWorkspace.workspaceId, provider: AgentProvider.GROK_BUILD }
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(context, { configurationFiles: [join(environment.GROK_HOME!, 'config.toml')], privateDirectories: [leapmuxServer.agentEnv.GROK_HOME!], expectedCredential: environment.LEAPMUX_E2E_MODEL_API_KEY! })
})
