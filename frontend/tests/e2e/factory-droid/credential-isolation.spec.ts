import { join } from 'node:path'
import { droidTest } from '../droid-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { nativeContext } from './scenarios'

droidTest('runs the actual native turn with private configuration and mock credentials', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  const environment = leapmuxServer.agentEnv
  const home = environment.FACTORY_HOME_OVERRIDE
  if (!home)
    throw new Error('The private provider configuration is absent.')
  await exerciseCredentialIsolation(context, {
    privateDirectories: [home],
    expectedCredential: MODEL_KEY,
    configurationFiles: [join(home, '.factory', 'settings.json')],
    configurationMarkers: ['droid-e2e'],
  })
})
