import { join } from 'node:path'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest('runs the actual native turn with private configuration and mock credentials', async ({ authenticatedQoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedQoderWorkspace.workspaceId })
  const environment = leapmuxServer.agentEnv
  const home = environment.HOME
  if (!home)
    throw new Error('The private provider configuration is absent.')
  await exerciseCredentialIsolation(context, {
    privateDirectories: [home],
    expectedCredential: MODEL_KEY,
    configurationFiles: [join(home, '.qoder', 'settings.json')],
    configurationMarkers: ['mockprov'],
  })
})
