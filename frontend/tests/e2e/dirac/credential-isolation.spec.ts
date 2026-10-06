import { diracTest } from '../dirac-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

diracTest('runs the actual native turn with private configuration and mock credentials', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  const home = environment.DIRAC_DIR
  if (!home)
    throw new Error('The private provider configuration is absent.')
  await exerciseCredentialIsolation(native, {
    privateDirectories: [home],
    expectedCredential: MODEL_KEY,
    inlineConfiguration: [JSON.stringify({ provider: environment.DIRAC_PROVIDER, model: environment.DIRAC_MODEL, apiKey: environment.DIRAC_API_KEY, baseUrl: environment.DIRAC_BASE_URL })],
    configurationMarkers: ['deepseek-flash'],
  })
})
