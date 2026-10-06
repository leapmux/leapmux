import { claudeTest } from '../claude-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

claudeTest('uses private provider configuration and the actual mock credential', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(native, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [environment.HOME!, environment.CLAUDE_CONFIG_DIR!],
    inlineConfiguration: [environment.ANTHROPIC_BASE_URL!, environment.ANTHROPIC_API_KEY!],
  })
})
