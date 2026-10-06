import { join } from 'node:path'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { nativeContext } from './scenarios'

deepseekHarnessTest('uses private native configuration and the exact mock credential', async ({ authenticatedDeepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDeepseekHarnessWorkspace.workspaceId })
  const home = leapmuxServer.agentEnv.DSH_HOME
  if (!home)
    throw new Error('The private DeepSeek Harness home is absent.')
  await exerciseCredentialIsolation(context, { privateDirectories: [home], expectedCredential: MODEL_KEY, configurationFiles: [join(home, 'cordis.patch.yml')], inlineConfiguration: [`DEEPSEEK_API_KEY=${leapmuxServer.agentEnv.DEEPSEEK_API_KEY}`], configurationMarkers: ['deepseek-official', 'deepseek-flash'] })
})
