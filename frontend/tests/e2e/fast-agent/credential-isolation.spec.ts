import { join } from 'node:path'
import { fastAgentTest } from '../fastagent-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { nativeContext } from './scenarios'

fastAgentTest('runs the actual native turn with private configuration and mock credentials', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  const environment = leapmuxServer.agentEnv
  const home = environment.FAST_AGENT_HOME
  if (!home)
    throw new Error('The private provider configuration is absent.')
  await exerciseCredentialIsolation(context, {
    privateDirectories: [home],
    expectedCredential: MODEL_KEY,
    configurationFiles: [join(home, 'fast-agent.yaml')],
    configurationMarkers: ['gpt-4o'],
  })
})
