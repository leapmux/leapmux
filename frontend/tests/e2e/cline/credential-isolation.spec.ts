import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { clineTest } from '../cline-fixtures'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

clineTest('loads private native configuration and calls only the suite mock', async ({ page, modelScript, leapmuxServer, authenticatedClineWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedClineWorkspace.workspaceId, provider: AgentProvider.CLINE }
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(context, { configurationFiles: [join(environment.CLINE_DATA_DIR!, 'settings', 'providers.json')], privateDirectories: [leapmuxServer.agentEnv.CLINE_DATA_DIR!], expectedCredential: environment.LEAPMUX_E2E_MODEL_API_KEY! })
})
