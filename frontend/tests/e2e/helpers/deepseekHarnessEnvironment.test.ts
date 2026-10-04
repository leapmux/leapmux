import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { createDeepseekHarnessEnvironment, DEEPSEEK_HARNESS_MODEL_ID } from './deepseekHarnessEnvironment'

const directories: string[] = []
function directory(): string {
  const root = join(import.meta.dirname, '../../../../.tmp/deepseek-harness-provider/helper-tests')
  mkdirSync(root, { recursive: true })
  const path = mkdtempSync(join(root, 'environment-'))
  directories.push(path)
  return path
}

afterEach(() => {
  for (const path of directories.splice(0))
    rmSync(path, { recursive: true, force: true })
})

describe('createDeepseekHarnessEnvironment', () => {
  it('writes a private native profile with exact local credentials', () => {
    const runDirectory = directory()
    const environment = createDeepseekHarnessEnvironment({ runDirectory, modelURL: 'http://127.0.0.1:4567', modelKey: 'isolated-key' })
    expect(environment.DSH_HOME).toBe(join(runDirectory, 'deepseek-harness-home'))
    expect(environment.DEEPSEEK_BASE_URL).toBe('http://127.0.0.1:4567')
    expect(environment.DEEPSEEK_API_KEY).toBe('isolated-key')
    expect(environment.LEAPMUX_DEEPSEEK_HARNESS_DEFAULT_MODEL).toBe(DEEPSEEK_HARNESS_MODEL_ID)
    const profile: unknown = JSON.parse(readFileSync(join(environment.DSH_HOME!, 'cordis.patch.yml'), 'utf8'))
    expect(profile).toEqual(expect.arrayContaining([
      { id: 'session-title-llm', disabled: true },
      { id: 'agent-default-model', config: { provider: 'deepseek-official', model: 'deepseek-flash', reasoningEffort: 'high' } },
    ]))
    expect(JSON.stringify(profile)).toContain('http://127.0.0.1:4567')
    expect(JSON.stringify(profile)).not.toContain('isolated-key')
  })

  it.each(['standard', 'ptc'] as const)('writes the selected native preset only in its private profile: %s', (agentPreset) => {
    const environment = createDeepseekHarnessEnvironment({ runDirectory: directory(), modelURL: 'http://127.0.0.1:4567', modelKey: 'key', agentPreset })
    const profile: unknown = JSON.parse(readFileSync(join(environment.DSH_HOME!, 'cordis.patch.yml'), 'utf8'))
    expect(profile).toEqual(expect.arrayContaining([
      { id: 'agent-preset-registry', config: { default: agentPreset } },
    ]))
  })

  it.each(['https://127.0.0.1:4567', 'http://example.com:4567', 'http://user:secret@127.0.0.1:4567', 'http://127.0.0.1:4567/?token=private', 'http://127.0.0.1:4567/#fragment'])('refuses the unsafe model endpoint %s', (modelURL) => {
    expect(() => createDeepseekHarnessEnvironment({ runDirectory: directory(), modelURL, modelKey: 'key' })).toThrow('loopback HTTP URL')
  })

  it('refuses relative directories and missing credentials', () => {
    expect(() => createDeepseekHarnessEnvironment({ runDirectory: 'relative', modelURL: 'http://127.0.0.1:4567', modelKey: 'key' })).toThrow('absolute')
    expect(() => createDeepseekHarnessEnvironment({ runDirectory: directory(), modelURL: 'http://127.0.0.1:4567', modelKey: '' })).toThrow('must be present')
  })

  it('configures the existing MCP servers without changing their arguments', () => {
    const environment = createDeepseekHarnessEnvironment({ runDirectory: directory(), modelURL: 'http://127.0.0.1:4567', modelKey: 'key', mcpServers: [{ name: 'echo', command: '/native/node', args: ['/private/echo server.js', '--literal=$HOME'] }] })
    const profile: unknown = JSON.parse(readFileSync(join(environment.DSH_HOME!, 'cordis.patch.yml'), 'utf8'))
    expect(profile).toEqual(expect.arrayContaining([{ insert: [expect.objectContaining({ config: expect.objectContaining({ transport: 'stdio', serverName: 'echo', command: '/native/node', args: ['/private/echo server.js', '--literal=$HOME'], failOnStartupError: true }) })] }]))
  })

  it.each([
    { mcpServers: [{ name: 'echo', command: '/node', args: [] }, { name: 'echo', command: '/node', args: [] }] },
    { mcpServers: [{ name: '../escape', command: '/node', args: [] }] },
    { mcpServers: [{ name: 'echo', command: 'relative-node', args: [] }] },
  ])('refuses invalid or repeated MCP server values', ({ mcpServers }) => {
    expect(() => createDeepseekHarnessEnvironment({ runDirectory: directory(), modelURL: 'http://127.0.0.1:4567', modelKey: 'key', mcpServers })).toThrow()
  })
})
