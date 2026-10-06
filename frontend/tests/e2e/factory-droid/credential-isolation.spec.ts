import { join } from 'node:path'
import { droidTest } from '../droid-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

droidTest('runs the actual native turn with private configuration and mock credentials', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  const home = environment.FACTORY_HOME_OVERRIDE
  if (!home)
    throw new Error('The private provider configuration is absent.')
  await exerciseCredentialIsolation(native, {
    privateDirectories: [home],
    expectedCredential: MODEL_KEY,
    configurationFiles: [join(home, '.factory', 'settings.json')],
    configurationMarkers: ['droid-e2e'],
  })
})
