import { join } from 'node:path'
import { commandCodeTest } from '../command-code-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { nativeContext } from './scenarios'

commandCodeTest('uses private native configuration and exact mock credentials', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  const home = leapmuxServer.agentEnv.HOME
  if (!home)
    throw new Error('The private native HOME is absent.')
  await exerciseCredentialIsolation(context, { privateDirectories: [join(home, '.commandcode')], expectedCredential: MODEL_KEY, configurationFiles: [join(home, '.commandcode/providers.json')], inlineConfiguration: [`COMMAND_CODE_MOCK_KEY=${leapmuxServer.agentEnv.COMMAND_CODE_MOCK_KEY}`], configurationMarkers: ['command-code-e2e'] })
})
