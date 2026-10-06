import { join } from 'node:path'
import { expect } from '@playwright/test'
import { geminiTest } from '../gemini-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'

geminiTest('uses private native settings and a file credential through the local model API', async ({ native, leapmuxServer }) => {
  const env = leapmuxServer.agentEnv
  expect(env.GEMINI_FORCE_FILE_STORAGE).toBe('true')
  expect(env.GEMINI_CLI_NO_RELAUNCH).toBe('1')
  expect(env.GEMINI_TELEMETRY_ENABLED).toBe('false')
  await exerciseCredentialIsolation(native, { expectedCredential: MODEL_KEY, privateDirectories: [env.HOME!, env.GEMINI_CLI_HOME!], configurationFiles: [join(env.GEMINI_CLI_HOME!, '.gemini/settings.json'), env.GEMINI_CLI_SYSTEM_SETTINGS_PATH!], inlineConfiguration: [env.GOOGLE_GEMINI_BASE_URL!, env.GEMINI_API_KEY!], configurationMarkers: ['gemini-api-key'] })
})
