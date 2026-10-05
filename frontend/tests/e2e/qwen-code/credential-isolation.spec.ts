import { join } from 'node:path'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

qwenTest('loads private native configuration and calls only the suite mock', async ({ page, modelScript, leapmuxServer, authenticatedQwenWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedQwenWorkspace.workspaceId, provider: AgentProvider.QWEN_CODE }
  const environment = leapmuxServer.agentEnv
  // Qwen reads the API key from the environment variable that the provider's `envKey` states.
  // settings.json therefore holds the variable name, and the private environment holds the key.
  // The expected key is the suite constant, so the proof compares the private environment with it.
  await exerciseCredentialIsolation(context, {
    configurationFiles: [join(environment.QWEN_HOME!, 'settings.json')],
    inlineConfiguration: [environment.LEAPMUX_E2E_MODEL_API_KEY!],
    configurationMarkers: ['"envKey": "LEAPMUX_E2E_MODEL_API_KEY"'],
    privateDirectories: [environment.QWEN_HOME!],
    expectedCredential: MODEL_KEY,
  })
})
