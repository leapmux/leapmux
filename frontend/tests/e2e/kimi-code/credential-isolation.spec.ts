import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { kimiTest } from '../kimi-fixtures'

kimiTest('loads private native configuration and calls only the suite mock', async ({ page, modelScript, leapmuxServer, authenticatedKimiWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKimiWorkspace.workspaceId, provider: AgentProvider.KIMI_CODE }
  const environment = leapmuxServer.agentEnv
  // Kimi Code reads the key from the environment variable that the configuration
  // identifies (`api_key_env`), never from a value stored in the file. So the
  // file must name the variable and must not hold the key. The accepted mock
  // credential of the turn request proves that Kimi read the key from the
  // variable.
  await exerciseCredentialIsolation(context, {
    configurationFiles: [join(environment.KIMI_CODE_HOME!, 'config.toml')],
    privateDirectories: [leapmuxServer.agentEnv.KIMI_CODE_HOME!],
    absentFromConfiguration: [environment.LEAPMUX_E2E_MODEL_API_KEY!],
    configurationMarkers: ['api_key_env = "LEAPMUX_E2E_MODEL_API_KEY"'],
  })
})
