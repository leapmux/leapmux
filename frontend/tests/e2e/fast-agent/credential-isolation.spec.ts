import { join } from 'node:path'
import { fastAgentTest } from '../fastagent-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

fastAgentTest('runs the actual native turn with private configuration and mock credentials', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  const home = environment.FAST_AGENT_HOME
  if (!home)
    throw new Error('The private provider configuration is absent.')
  await exerciseCredentialIsolation(native, {
    privateDirectories: [home],
    expectedCredential: MODEL_KEY,
    configurationFiles: [join(home, 'fast-agent.yaml')],
    configurationMarkers: ['gpt-4o'],
  })
})
