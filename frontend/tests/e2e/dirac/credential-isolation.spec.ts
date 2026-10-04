import { diracTest } from '../dirac-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { nativeContext } from './scenarios'

diracTest('runs the actual native turn with private configuration and mock credentials', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  const environment = leapmuxServer.agentEnv
  const home = environment.DIRAC_DIR
  if (!home)
    throw new Error('The private provider configuration is absent.')
  await exerciseCredentialIsolation(context, {
    privateDirectories: [home],
    expectedCredential: MODEL_KEY,
    inlineConfiguration: [JSON.stringify({ provider: environment.DIRAC_PROVIDER, model: environment.DIRAC_MODEL, apiKey: environment.DIRAC_API_KEY, baseUrl: environment.DIRAC_BASE_URL })],
    configurationMarkers: ['deepseek-flash'],
  })
})
