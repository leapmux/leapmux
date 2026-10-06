import { join } from 'node:path'
import { gooseTest } from '../goose-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

gooseTest('uses private provider configuration and the actual mock credential', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(native, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!, environment.GOOSE_PATH_ROOT!],
    configurationFiles: [join(environment.GOOSE_PATH_ROOT!, 'config', 'config.yaml')],
    inlineConfiguration: [environment.OPENAI_BASE_URL!, environment.OPENAI_API_KEY!],
  })
})
