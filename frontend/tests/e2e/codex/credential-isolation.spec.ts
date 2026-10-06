import { join } from 'node:path'
import { codexTest } from '../codex-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

codexTest('uses private provider configuration and the actual mock credential', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(native, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!, environment.CODEX_HOME!],
    configurationFiles: [join(environment.CODEX_HOME!, 'config.toml')],
    inlineConfiguration: [environment.LEAPMUX_E2E_MODEL_API_KEY!],
    configurationMarkers: ['LEAPMUX_E2E_MODEL_API_KEY'],
  })
})
