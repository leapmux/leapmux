import { cursorTest } from '../cursor-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

cursorTest('uses private provider configuration and the actual mock credential', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(native, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!, environment.CURSOR_CONFIG_DIR!],
    inlineConfiguration: [environment.CURSOR_API_ENDPOINT!, environment.CURSOR_AUTH_TOKEN!, environment.AGENT_CLI_CREDENTIAL_STORE!],
    configurationMarkers: ['memory'],
  })
})
