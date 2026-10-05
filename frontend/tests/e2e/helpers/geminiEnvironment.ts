import { mkdirSync, writeFileSync } from 'node:fs'
import { isAbsolute, join } from 'node:path'

export interface GeminiEnvironmentOptions {
  runDirectory: string
  modelURL: string
  modelKey: string
  modelID: string
  /** Use the existing shared Model Context Protocol server commands. */
  mcpServers?: readonly { name: string, command: string, args: readonly string[] }[]
}

/** Create Gemini's private settings without account or credential-store access. */
export function createGeminiEnvironment(options: GeminiEnvironmentOptions): Record<string, string> {
  if (!isAbsolute(options.runDirectory))
    throw new Error('The Gemini run directory must be absolute.')
  const endpoint = new URL(options.modelURL)
  if (endpoint.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(endpoint.hostname)
    || endpoint.username || endpoint.password || endpoint.pathname !== '/' || endpoint.search || endpoint.hash) {
    throw new Error('The Gemini model endpoint must be a loopback HTTP origin.')
  }
  if (!options.modelKey || !options.modelID)
    throw new Error('The Gemini mock model key and model ID must be present.')
  const home = join(options.runDirectory, 'gemini-home')
  const directory = join(home, '.gemini')
  const systemSettings = join(options.runDirectory, 'gemini-system-settings.json')
  const mcpServers: Record<string, { command: string, args: string[] }> = {}
  for (const server of options.mcpServers ?? []) {
    if (!/^[\w-]{1,64}$/.test(server.name) || Object.hasOwn(mcpServers, server.name))
      throw new Error('The Gemini MCP server names must be valid and distinct.')
    if (!isAbsolute(server.command))
      throw new Error('The Gemini MCP command must be absolute.')
    mcpServers[server.name] = { command: server.command, args: [...server.args] }
  }
  mkdirSync(directory, { recursive: true, mode: 0o700 })
  writeFileSync(systemSettings, '{}\n', { mode: 0o600 })
  writeFileSync(join(directory, 'settings.json'), `${JSON.stringify({
    security: { auth: { selectedType: 'gemini-api-key' }, folderTrust: { enabled: false } },
    privacy: { usageStatisticsEnabled: false },
    telemetry: { enabled: false },
    // Gemini CLI installs a release with `npm install -g` from its interactive UI unless
    // enableAutoUpdate is false, and checks for one unless enableAutoUpdateNotification is
    // false. `--acp` never reaches that UI. The deprecated disableAutoUpdate and
    // disableUpdateNag keys work only through a migration that rewrites this file.
    general: { enableAutoUpdate: false, enableAutoUpdateNotification: false, plan: { enabled: true, modelRouting: false } },
    advanced: { autoConfigureMemory: false },
    model: { name: options.modelID, skipNextSpeakerCheck: true },
    tools: { shell: { enableInteractiveShell: false } },
    mcpServers,
  }, null, 2)}\n`, { mode: 0o600 })
  return {
    GEMINI_CLI_HOME: home,
    GEMINI_FORCE_FILE_STORAGE: 'true',
    GEMINI_CLI_NO_RELAUNCH: '1',
    GEMINI_CLI_SYSTEM_SETTINGS_PATH: systemSettings,
    GEMINI_CLI_SYSTEM_DEFAULTS_PATH: systemSettings,
    GEMINI_API_KEY: options.modelKey,
    GOOGLE_API_KEY: '',
    GOOGLE_APPLICATION_CREDENTIALS: join(directory, 'unused-application-credentials.json'),
    GOOGLE_GENAI_USE_VERTEXAI: 'false',
    GOOGLE_CLOUD_PROJECT: '',
    GOOGLE_CLOUD_LOCATION: '',
    GOOGLE_GEMINI_BASE_URL: endpoint.origin,
    GEMINI_TELEMETRY_ENABLED: 'false',
    LEAPMUX_GEMINI_DEFAULT_MODEL: options.modelID,
  }
}
