import { join } from 'node:path'
import { clineTest } from '../cline-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

clineTest('loads private native configuration and calls only the suite mock', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(native, { configurationFiles: [join(environment.CLINE_DATA_DIR!, 'settings', 'providers.json')], privateDirectories: [environment.CLINE_DATA_DIR!], expectedCredential: MODEL_KEY })
})
