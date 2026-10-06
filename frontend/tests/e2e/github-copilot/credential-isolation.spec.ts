import { copilotTest } from '../copilot-fixtures'
import { MOCK_COPILOT_GITHUB_TOKEN } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

copilotTest('uses private provider configuration and the actual mock credential', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  await exerciseCredentialIsolation(native, {
    expectedCredential: MOCK_COPILOT_GITHUB_TOKEN,
    privateDirectories: [environment.HOME!, environment.COPILOT_HOME!],
    inlineConfiguration: [environment.COPILOT_API_URL!, environment.COPILOT_GITHUB_TOKEN!],
  })
})
