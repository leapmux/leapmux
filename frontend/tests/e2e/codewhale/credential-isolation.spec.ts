import { join } from 'node:path'
import { codewhaleTest } from '../codewhale-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

codewhaleTest('loads private native configuration and calls only the suite mock', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(native, { configurationFiles: [join(environment.CODEWHALE_HOME!, 'config.toml')], privateDirectories: [environment.CODEWHALE_HOME!], expectedCredential: MODEL_KEY })
})
