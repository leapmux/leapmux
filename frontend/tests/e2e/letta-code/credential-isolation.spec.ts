import { join } from 'node:path'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { lettaTest } from '../letta-fixtures'

lettaTest('runs the actual native turn with private configuration and mock credentials', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  const home = environment.LETTA_LOCAL_BACKEND_DIR
  if (!home)
    throw new Error('The private provider configuration is absent.')
  await exerciseCredentialIsolation(native, {
    privateDirectories: [home],
    expectedCredential: MODEL_KEY,
    configurationFiles: [join(home, 'providers', 'auth.json')],
    configurationMarkers: ['openai-compatible'],
  })
})
