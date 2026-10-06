import { join } from 'node:path'
import { expect } from '@playwright/test'
import { geminiTest } from '../gemini-fixtures'
import { MODEL_KEY } from '../helpers/mockAgentEnvironment'
import { exerciseCredentialIsolation } from '../helpers/nativeCredentialIsolation'
import { nativeContext } from './scenarios'

geminiTest('uses private native settings and a file credential through the local model API', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  const env = leapmuxServer.agentEnv
  if (!env)
    throw new Error('The credential scenario requires the isolated Gemini environment.')
  expect(env.GEMINI_FORCE_FILE_STORAGE).toBe('true')
  expect(env.GEMINI_CLI_NO_RELAUNCH).toBe('1')
  expect(env.GEMINI_TELEMETRY_ENABLED).toBe('false')
  await exerciseCredentialIsolation(context, { expectedCredential: MODEL_KEY, privateDirectories: [env.HOME!, env.GEMINI_CLI_HOME!], configurationFiles: [join(env.GEMINI_CLI_HOME!, '.gemini/settings.json'), env.GEMINI_CLI_SYSTEM_SETTINGS_PATH!], inlineConfiguration: [env.GOOGLE_GEMINI_BASE_URL!, env.GEMINI_API_KEY!], configurationMarkers: ['gemini-api-key'] })
})
