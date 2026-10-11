import { join } from 'node:path'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { kimiTest } from '../kimi-fixtures'

kimiTest('loads private native configuration and calls only the suite mock', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  // Kimi Code reads the key from the environment variable that the configuration
  // identifies (`api_key_env`), never from a value stored in the file. So the
  // file must identify the variable and must not hold the key. The accepted mock
  // credential of the turn request proves that Kimi read the key from the
  // variable.
  await exerciseCredentialIsolation(native, {
    configurationFiles: [join(environment.KIMI_CODE_HOME!, 'config.toml')],
    privateDirectories: [environment.KIMI_CODE_HOME!],
    absentFromConfiguration: [MODEL_KEY],
    configurationMarkers: ['api_key_env = "LEAPMUX_E2E_MODEL_API_KEY"'],
  })
})
