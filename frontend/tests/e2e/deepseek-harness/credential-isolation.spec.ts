import { join } from 'node:path'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

deepseekHarnessTest('uses private native configuration and the exact mock credential', async ({ native, leapmuxServer }) => {
  const home = leapmuxServer.agentEnv.DSH_HOME
  if (!home)
    throw new Error('The private DeepSeek Harness home is absent.')
  await exerciseCredentialIsolation(native, { privateDirectories: [home], expectedCredential: MODEL_KEY, configurationFiles: [join(home, 'cordis.patch.yml')], inlineConfiguration: [`DEEPSEEK_API_KEY=${leapmuxServer.agentEnv.DEEPSEEK_API_KEY}`], configurationMarkers: ['deepseek-official', 'deepseek-flash'] })
})
