import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { mimoTest } from '../mimo-fixtures'

mimoTest('loads private native configuration and calls only the suite mock', async ({ page, modelScript, leapmuxServer, authenticatedMiMoWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(context, { inlineConfiguration: [environment.MIMOCODE_CONFIG_CONTENT!], privateDirectories: [leapmuxServer.agentEnv.MIMOCODE_HOME!], expectedCredential: environment.LEAPMUX_E2E_MODEL_API_KEY! })
})
