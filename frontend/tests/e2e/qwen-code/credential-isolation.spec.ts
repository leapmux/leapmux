import { join } from 'node:path'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { qwenTest } from '../qwen-fixtures'

qwenTest('loads private native configuration and calls only the suite mock', async ({ native, leapmuxServer }) => {
  const environment = leapmuxServer.agentEnv
  // Qwen reads the API key from the environment variable that the provider's `envKey` states.
  // settings.json therefore holds the variable name, and the private environment holds the key.
  // The expected key is the suite constant, so the proof compares the private environment with it.
  await exerciseCredentialIsolation(native, {
    configurationFiles: [join(environment.QWEN_HOME!, 'settings.json')],
    inlineConfiguration: [environment.LEAPMUX_E2E_MODEL_API_KEY!],
    configurationMarkers: ['"envKey": "LEAPMUX_E2E_MODEL_API_KEY"'],
    privateDirectories: [environment.QWEN_HOME!],
    expectedCredential: MODEL_KEY,
  })
})
