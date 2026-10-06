import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { createGeminiEnvironment } from './geminiEnvironment'

const directories: string[] = []
function directory(): string {
  const root = join(import.meta.dirname, '../../../../.tmp/gemini-cli-provider/helper-tests')
  mkdirSync(root, { recursive: true })
  const path = mkdtempSync(join(root, 'environment-'))
  directories.push(path)
  return path
}

afterEach(() => {
  for (const path of directories.splice(0))
    rmSync(path, { recursive: true, force: true })
})

describe('createGeminiEnvironment', () => {
  it('uses private native settings and file credentials with the exact local model', () => {
    const runDirectory = directory()
    const env = createGeminiEnvironment({ runDirectory, modelURL: 'http://127.0.0.1:4321', modelKey: 'private-key', modelID: 'gemini-2.5-pro' })
    expect(env.GEMINI_CLI_HOME).toBe(join(runDirectory, 'gemini-home'))
    expect(env.GEMINI_FORCE_FILE_STORAGE).toBe('true')
    expect(env.GEMINI_CLI_NO_RELAUNCH).toBe('1')
    expect(env.GEMINI_TELEMETRY_ENABLED).toBe('false')
    expect(env.GOOGLE_GEMINI_BASE_URL).toBe('http://127.0.0.1:4321')
    expect(env.GEMINI_API_KEY).toBe('private-key')
    expect(env.GOOGLE_API_KEY).toBe('')
    expect(env.GOOGLE_APPLICATION_CREDENTIALS).toBe(join(runDirectory, 'gemini-home/.gemini/unused-application-credentials.json'))
    expect(env.LEAPMUX_GEMINI_DEFAULT_MODEL).toBe('gemini-2.5-pro')
    expect(readFileSync(env.GEMINI_CLI_SYSTEM_SETTINGS_PATH!, 'utf8')).toBe('{}\n')
    const settings: unknown = JSON.parse(readFileSync(join(env.GEMINI_CLI_HOME!, '.gemini/settings.json'), 'utf8'))
    expect(settings).toMatchObject({ security: { auth: { selectedType: 'gemini-api-key' }, folderTrust: { enabled: false } }, telemetry: { enabled: false }, privacy: { usageStatisticsEnabled: false }, general: { plan: { enabled: true, modelRouting: false } }, model: { name: 'gemini-2.5-pro', skipNextSpeakerCheck: true } })
    expect(JSON.stringify(settings)).not.toContain('private-key')
  })

  it('turns the update check and the update off with the canonical settings keys', () => {
    // Gemini CLI 0.62.0 installs a release with `npm install -g` from its interactive
    // UI unless `general.enableAutoUpdate` is false, and checks for one unless
    // `general.enableAutoUpdateNotification` is false. It honors the deprecated
    // `disableAutoUpdate` and `disableUpdateNag` only through a migration that
    // rewrites settings.json on each start, which a later release can drop.
    const env = createGeminiEnvironment({ runDirectory: directory(), modelURL: 'http://127.0.0.1:4321', modelKey: 'key', modelID: 'model' })
    const settings = JSON.parse(readFileSync(join(env.GEMINI_CLI_HOME!, '.gemini/settings.json'), 'utf8')) as { general: Record<string, unknown> }
    expect(settings.general).toMatchObject({ enableAutoUpdate: false, enableAutoUpdateNotification: false })
    expect(settings.general).not.toHaveProperty('disableAutoUpdate')
    expect(settings.general).not.toHaveProperty('disableUpdateNag')
  })

  it('uses the existing MCP commands and preserves their literal arguments', () => {
    const env = createGeminiEnvironment({ runDirectory: directory(), modelURL: 'http://localhost:4321', modelKey: 'key', modelID: 'model', mcpServers: [{ name: 'result_probe', command: '/private/node', args: ['/private/server file.mjs', '--literal=$HOME'] }] })
    const settings: unknown = JSON.parse(readFileSync(join(env.GEMINI_CLI_HOME!, '.gemini/settings.json'), 'utf8'))
    expect(settings).toMatchObject({ mcpServers: { result_probe: { command: '/private/node', args: ['/private/server file.mjs', '--literal=$HOME'] } } })
  })

  it.each(['https://127.0.0.1:4321', 'http://google.example:4321', 'http://user:key@127.0.0.1:4321', 'http://127.0.0.1:4321/v1', 'http://127.0.0.1:4321/?key=secret', 'http://127.0.0.1:4321/#secret'])('refuses the unsafe model endpoint %s', (modelURL) => {
    expect(() => createGeminiEnvironment({ runDirectory: directory(), modelURL, modelKey: 'key', modelID: 'model' })).toThrow('loopback HTTP origin')
  })

  it('refuses relative directories and absent model credentials or IDs', () => {
    expect(() => createGeminiEnvironment({ runDirectory: 'relative', modelURL: 'http://localhost:4321', modelKey: 'key', modelID: 'model' })).toThrow('absolute')
    for (const [modelKey, modelID] of [['', 'model'], ['key', '']])
      expect(() => createGeminiEnvironment({ runDirectory: directory(), modelURL: 'http://localhost:4321', modelKey: modelKey!, modelID: modelID! })).toThrow('must be present')
  })

  it.each([
    { mcpServers: [{ name: 'same', command: '/node', args: [] }, { name: 'same', command: '/node', args: [] }], error: 'The Gemini MCP server names must be valid and distinct.' },
    { mcpServers: [{ name: '../escape', command: '/node', args: [] }], error: 'The Gemini MCP server names must be valid and distinct.' },
    { mcpServers: [{ name: 'server', command: 'relative', args: [] }], error: 'The Gemini MCP command must be absolute.' },
  ])('refuses invalid MCP names or commands', ({ mcpServers, error }) => {
    expect(() => createGeminiEnvironment({ runDirectory: directory(), modelURL: 'http://localhost:4321', modelKey: 'key', modelID: 'model', mcpServers })).toThrow(error)
  })
})
