import { spawnSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { pathToFileURL } from 'node:url'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { createMuseEnvironment } from './museEnvironment'
import { writeNodeLauncher } from './nodeLauncher'

let directory: string
let home: string
beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  directory = mkdtempSync(join(scratch, 'muse-environment-test-'))
  home = join(directory, 'home')
  mkdirSync(home)
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

function options() {
  return { runDirectory: directory, homeDir: home, shimsDirectory: join(directory, 'shims'), baseURL: 'http://127.0.0.1:4567/v1', modelKey: 'mock-key', modelID: 'muse-spark-1.2', searchPath: '' }
}

describe('createMuseEnvironment', () => {
  it.each([
    { input: ['serve', '--trust-workspace'], expected: ['serve', '--trust-workspace', '--disable-sandbox'] },
    { input: ['--version'], expected: ['--version'] },
  ])('preserves native arguments and adds only a supported test sandbox option: $input', ({ input, expected }) => {
    const nativeDirectory = join(directory, 'native')
    mkdirSync(nativeDirectory)
    const script = join(nativeDirectory, 'arguments.mjs')
    writeFileSync(script, 'process.stdout.write(JSON.stringify(process.argv.slice(2)))\n')
    const nativeBinary = writeNodeLauncher(nativeDirectory, 'native-muse', { node: process.execPath, script })
    createMuseEnvironment({ ...options(), nativeBinary })
    const result = spawnSync(join(directory, 'shims', process.platform === 'win32' ? 'muse.cmd' : 'muse'), input, {
      encoding: 'utf8',
      timeout: 30000,
      shell: process.platform === 'win32',
    })
    expect(result.error).toBeUndefined()
    expect(result.status, result.stderr).toBe(0)
    expect(JSON.parse(result.stdout)).toEqual(expected)
  })

  it.each([true, false])('imports the private writer and mock server in a fresh Bun process with writer first: %s', (writerFirst) => {
    const writer = pathToFileURL(join(import.meta.dirname, 'museEnvironment.ts')).href
    const server = pathToFileURL(join(import.meta.dirname, 'mockModelServer.ts')).href
    const imports = writerFirst ? [writer, server] : [server, writer]
    const script = imports.map(path => `await import(${JSON.stringify(path)})`).join('\n')
    const result = spawnSync('bun', ['--eval', script], { cwd: resolve(import.meta.dirname, '../../..'), encoding: 'utf8', timeout: 30000 })
    expect(result.error).toBeUndefined()
    expect(result.stderr).not.toContain('ENOENT reading')
    expect(result.status, result.stderr).toBe(0)
  })
  it('pins the exact private endpoint and keeps the mock key outside the settings file', () => {
    expect(createMuseEnvironment(options())).toEqual({ META_API_KEY: 'mock-key', MUSE_NO_AUTO_UPDATE: '1' })
    const path = join(home, '.config/muse/settings.json')
    const settings = JSON.parse(readFileSync(path, 'utf8'))
    expect(settings.endpoint_transport).toEqual({ base_url: 'http://127.0.0.1:4567/v1', auth: 'bearer' })
    expect(settings.context).toEqual({ foreign_personal_rules: false, foreign_personal_skills: false })
    expect(settings.run.reminder_roster.agents).toEqual([])
    expect(settings.telemetry.enabled).toBe(false)
    expect(readFileSync(path, 'utf8')).not.toContain('mock-key')
    expect(statSync(path).mode & 0o777).toBe(0o600)
  })

  it.each(['https://example.com/v1', 'http://user:pass@127.0.0.1/v1', 'http://127.0.0.1/v1?key=x', 'http://127.0.0.1/not-v1'])('refuses an invalid endpoint before any write %s', (baseURL) => {
    expect(() => createMuseEnvironment({ ...options(), baseURL })).toThrow(/must be a loopback HTTP URL\.|must end with \/v1\./)
    expect(existsSync(join(home, '.config'))).toBe(false)
  })

  it('writes the actual standard MCP settings shape', () => {
    createMuseEnvironment({ ...options(), mcpServers: [{ name: 'echo_probe', command: '/private/node', args: ['/private/server.mjs'] }] })
    const settings = JSON.parse(readFileSync(join(home, '.config/muse/settings.json'), 'utf8'))
    expect(settings.mcpServers).toEqual({ echo_probe: { command: '/private/node', args: ['/private/server.mjs'] } })
  })

  it('refuses a configuration link outside the private run', () => {
    const foreign = mkdtempSync(join(resolve(import.meta.dirname, '../../../../.tmp'), 'muse-foreign-home-'))
    try {
      symlinkSync(foreign, join(home, '.config'), 'dir')
      expect(() => createMuseEnvironment(options())).toThrow('The private native path resolves outside the E2E run.')
      expect(existsSync(join(foreign, 'muse'))).toBe(false)
    }
    finally {
      rmSync(foreign, { recursive: true, force: true })
    }
  })
})
