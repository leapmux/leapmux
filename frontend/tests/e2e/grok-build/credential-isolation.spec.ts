import { join } from 'node:path'
import { grokTest } from '../grok-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

grokTest('loads private native configuration and calls only the suite mock', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(native, { configurationFiles: [join(environment.GROK_HOME!, 'config.toml')], privateDirectories: [environment.GROK_HOME!], expectedCredential: MODEL_KEY })
})
