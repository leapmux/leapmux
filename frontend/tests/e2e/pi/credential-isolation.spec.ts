import { join } from 'node:path'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { piTest } from '../pi-fixtures'

piTest('uses private provider configuration and the actual mock credential', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(native, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!, environment.PI_CODING_AGENT_DIR!],
    configurationFiles: [join(environment.PI_CODING_AGENT_DIR!, 'models.json'), join(environment.PI_CODING_AGENT_DIR!, 'settings.json'), join(environment.PI_CODING_AGENT_DIR!, 'mcp.json')],
  })
})
