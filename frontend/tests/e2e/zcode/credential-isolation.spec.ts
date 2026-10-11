import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('uses private provider configuration and the actual mock credential', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(native, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!, environment.ZCODE_STORAGE_DIR!],
    configurationFiles: [environment.ZCODE_PERSONAL_PROVIDER_CONFIG_FILE!],
  })
})
