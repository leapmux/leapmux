import { join } from 'node:path'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { qoderTest } from '../qoder-fixtures'

qoderTest('runs the actual native turn with private configuration and mock credentials', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  const home = environment.HOME
  if (!home)
    throw new Error('The private provider configuration is absent.')
  await exerciseCredentialIsolation(native, {
    privateDirectories: [home],
    expectedCredential: MODEL_KEY,
    configurationFiles: [join(home, '.qoder', 'settings.json')],
    configurationMarkers: ['mockprov'],
  })
})
