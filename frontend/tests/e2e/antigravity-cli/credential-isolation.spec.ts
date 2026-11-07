import { join } from 'node:path'
import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

antigravityTest('uses private native settings and a file credential through the local model API', async ({ native, leapmuxServer }) => {
  const env = leapmuxServer.agentEnv
  expect(env.GOOGLE_GENAI_USE_VERTEXAI).toBe('false')
  await exerciseCredentialIsolation(native, {
    expectedCredential: MODEL_KEY,
    privateDirectories: [env.HOME!],
    configurationFiles: [
      join(env.HOME!, '.gemini', 'antigravity-cli', 'cache', 'acp-auth-state.json'),
    ],
    inlineConfiguration: [env.GOOGLE_GEMINI_BASE_URL!, env.GEMINI_API_KEY!],
    configurationMarkers: ['gemini-api-key'],
  })
})
