import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { mimoTest } from '../mimo-fixtures'

mimoTest('loads private native configuration and calls only the suite mock', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(native, { inlineConfiguration: [environment.MIMOCODE_CONFIG_CONTENT!], privateDirectories: [environment.MIMOCODE_HOME!], expectedCredential: MODEL_KEY })
})
