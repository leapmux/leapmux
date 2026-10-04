import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

kimiTest('loads private native configuration and calls only the suite mock', async ({ page, modelScript, leapmuxServer, authenticatedKimiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE }
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(context, { configurationFiles: [join(environment.KIMI_CODE_HOME!, 'config.toml')], privateDirectories: [leapmuxServer.agentEnv.KIMI_CODE_HOME!], expectedCredential: environment.LEAPMUX_E2E_MODEL_API_KEY! })
})
