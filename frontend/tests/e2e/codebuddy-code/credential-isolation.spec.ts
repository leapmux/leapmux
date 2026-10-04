import { join } from 'node:path'
import { codebuddyTest } from '../codebuddy-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { nativeContext } from './scenarios'

codebuddyTest('runs the actual native turn with private configuration and mock credentials', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
  const environment = leapmuxServer.agentEnv
  const home = environment.CODEBUDDY_CONFIG_DIR
  if (!home)
    throw new Error('The private provider configuration is absent.')
  await exerciseCredentialIsolation(context, {
    privateDirectories: [home],
    expectedCredential: MODEL_KEY,
    configurationFiles: [join(home, 'models.json')],
    configurationMarkers: ['deepseek-flash'],
  })
})
