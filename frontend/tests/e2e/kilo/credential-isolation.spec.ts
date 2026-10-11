import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { kiloTest } from '../kilo-fixtures'

kiloTest('uses private provider configuration and the actual mock credential', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(native, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!],
    inlineConfiguration: [environment.KILO_CONFIG_CONTENT!],
    configurationMarkers: ['leapmux-e2e'],
  })
})
