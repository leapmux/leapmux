import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { createCommandCodeEnvironment } from './commandCodeEnvironment'

const directories: string[] = []
function options() {
  const root = join(import.meta.dirname, '../../../../.tmp/command-code-provider/helper-tests')
  mkdirSync(root, { recursive: true })
  const runDirectory = mkdtempSync(join(root, 'environment-'))
  directories.push(runDirectory)
  return { runDirectory, modelURL: 'http://127.0.0.1:4567', modelKey: 'private-model-key', modelID: 'leapmux-e2e/command-code-e2e', alternateModelID: 'leapmux-e2e/command-code-e2e-alt' }
}

afterEach(() => {
  for (const directory of directories.splice(0))
    rmSync(directory, { recursive: true, force: true })
})

describe('createCommandCodeEnvironment', () => {
  it('writes a private native catalog with exact model and effort pins', () => {
    const input = options()
    const environment = createCommandCodeEnvironment(input)
    const config = join(input.runDirectory, 'agent-home/.commandcode')
    const providers: unknown = JSON.parse(readFileSync(join(config, 'providers.json'), 'utf8'))
    expect(providers).toEqual({ provider: { 'leapmux-e2e': {
      baseURL: 'http://127.0.0.1:4567/v1',
      api: 'openai-completions',
      apiKey: '$COMMAND_CODE_MOCK_KEY',
      models: {
        'command-code-e2e': { contextWindow: 131072, reasoning: true, reasoningEfforts: ['low', 'high'] },
        'command-code-e2e-alt': { contextWindow: 131072 },
      },
    } } })
    expect(readFileSync(join(config, 'providers.json'), 'utf8')).not.toContain(input.modelKey)
    expect(environment).toMatchObject({ HOME: join(input.runDirectory, 'agent-home'), COMMAND_CODE_MOCK_KEY: input.modelKey, CMD_LOCAL_ONLY: '1', OTEL_SDK_DISABLED: 'true', LEAPMUX_COMMANDCODE_DEFAULT_MODEL: input.modelID, LEAPMUX_COMMANDCODE_DEFAULT_EFFORT: 'high' })
    const settings: unknown = JSON.parse(readFileSync(join(config, 'settings.json'), 'utf8'))
    expect(settings).toEqual({ model: input.modelID, mods: { disabled: ['learning', 'titling', 'update-notice'] }, byokFeatureTasks: 'session' })
    if (process.platform !== 'win32')
      expect(statSync(join(config, 'providers.json')).mode & 0o777).toBe(0o600)
  })

  it('preserves native MCP commands and clears a previous server list', () => {
    const input = options()
    createCommandCodeEnvironment({ ...input, mcpServers: [{ name: 'echo', command: '/native/node', args: ['/private/echo server.js', '--literal=$HOME'] }] })
    const path = join(input.runDirectory, 'agent-home/.commandcode/mcp.json')
    const servers: unknown = JSON.parse(readFileSync(path, 'utf8'))
    expect(servers).toEqual({ mcpServers: { echo: { command: '/native/node', args: ['/private/echo server.js', '--literal=$HOME'] } } })
    createCommandCodeEnvironment(input)
    const cleared: unknown = JSON.parse(readFileSync(path, 'utf8'))
    expect(cleared).toEqual({ mcpServers: {} })
  })

  it.each(['https://127.0.0.1:4567', 'http://example.com:4567', 'http://user:secret@127.0.0.1:4567', 'http://127.0.0.1:4567/?token=private', 'http://127.0.0.1:4567/#fragment'])('refuses the unsafe endpoint %s before it writes config', (modelURL) => {
    const input = options()
    expect(() => createCommandCodeEnvironment({ ...input, modelURL })).toThrow('loopback HTTP URL')
    expect(existsSync(join(input.runDirectory, 'agent-home/.commandcode'))).toBe(false)
  })

  it.each([
    [{ runDirectory: 'relative' }, 'The Command Code run directory must be absolute.'],
    [{ modelKey: '' }, 'The Command Code mock model key must be present.'],
    [{ modelID: '' }, 'The Command Code mock model ID must contain a provider and a model.'],
    [{ modelID: '../model' }, 'The Command Code mock model ID must contain a provider and a model.'],
    [{ alternateModelID: 'another-provider/model' }, 'The Command Code mock models must be distinct models of the same provider.'],
    [{ alternateModelID: 'leapmux-e2e/command-code-e2e' }, 'The Command Code mock models must be distinct models of the same provider.'],
    [{ mcpServers: [{ name: 'echo', command: '/node', args: [] }, { name: 'echo', command: '/node', args: [] }] }, 'The Command Code MCP server names must be valid and distinct.'],
    [{ mcpServers: [{ name: '../escape', command: '/node', args: [] }] }, 'The Command Code MCP server names must be valid and distinct.'],
    [{ mcpServers: [{ name: 'echo', command: 'relative-node', args: [] }] }, 'The Command Code MCP command must be absolute.'],
  ])('refuses invalid config before it writes native files', (override, error) => {
    const input = options()
    expect(() => createCommandCodeEnvironment({ ...input, ...override })).toThrow(error)
    expect(existsSync(join(input.runDirectory, 'agent-home/.commandcode'))).toBe(false)
  })
})
