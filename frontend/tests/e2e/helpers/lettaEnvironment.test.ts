import type { ExecFileSyncOptions } from 'node:child_process'
import { Buffer } from 'node:buffer'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { exeExtensionOnPath } from './binaryOnPath'
import { createLettaEnvironment } from './lettaEnvironment'

const setup = vi.hoisted(() => ({ execute: vi.fn<(file: string, args?: readonly string[], options?: ExecFileSyncOptions) => Buffer>() }))

// The Letta setup commands run an installed CLI. These tests answer them with a recorder.
vi.mock('node:child_process', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:child_process')>()
  return {
    ...actual,
    execFile: (file: string, args: readonly string[], options: ExecFileSyncOptions, callback: (error: Error | null, stdout: string, stderr: string) => void) => {
      queueMicrotask(() => {
        try {
          callback(null, setup.execute(file, args, options).toString(), '')
        }
        catch (error) {
          callback(error instanceof Error ? error : new Error(String(error)), '', '')
        }
      })
      return new actual.ChildProcess()
    },
  }
})

let runDirectory: string
let previousPath: string | undefined
const temporaryEnv = { TMPDIR: '/unit/tmp', TEMP: '/unit/tmp', TMP: '/unit/tmp' }
const options = () => ({ runDirectory, homeDir: join(runDirectory, 'home'), shimsDirectory: join(runDirectory, 'shims'), baseURL: 'http://127.0.0.1:4567/v1', modelKey: 'unit-key', temporaryEnv })

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../../.tmp')
  mkdirSync(scratch, { recursive: true })
  runDirectory = mkdtempSync(join(scratch, 'letta-environment-test-'))
  previousPath = process.env.PATH
  // An empty search path holds no `letta`, so no setup command runs unless a test puts one there.
  process.env.PATH = join(runDirectory, 'no-binaries')
  setup.execute.mockReset()
  setup.execute.mockReturnValue(Buffer.alloc(0))
})

afterEach(() => {
  if (previousPath === undefined)
    delete process.env.PATH
  else
    process.env.PATH = previousPath
  rmSync(runDirectory, { recursive: true, force: true })
})

describe('createLettaEnvironment', () => {
  it('provides private ChatGPT credentials with the native account claim', async () => {
    const env = await createLettaEnvironment(options())
    const auth = JSON.parse(readFileSync(join(env.LETTA_LOCAL_BACKEND_DIR!, 'providers', 'auth.json'), 'utf8'))
    expect(auth.providers['openai-codex']).toMatchObject({ provider_type: 'chatgpt_oauth', auth: { type: 'oauth', accountId: 'leapmux-letta-mock-account' } })
    const payload = JSON.parse(Buffer.from(auth.providers['openai-codex'].auth.access.split('.')[1], 'base64').toString('utf8'))
    expect(payload['https://api.openai.com/auth'].chatgpt_account_id).toBe('leapmux-letta-mock-account')
    expect(env).not.toHaveProperty('NODE_OPTIONS')
  })

  it.skipIf(process.platform === 'win32')('keeps the HTTP redirect inside the private Letta launcher', async () => {
    const binaries = join(runDirectory, 'binaries')
    mkdirSync(binaries)
    writeFileSync(join(binaries, 'letta'), 'controlled setup boundary', { mode: 0o755 })
    process.env.PATH = binaries
    const env = await createLettaEnvironment(options())
    const launcher = readFileSync(join(options().shimsDirectory, 'letta'), 'utf8')
    expect(launcher).toContain('NODE_OPTIONS=')
    expect(launcher).toContain('exec ')
    expect(launcher).toContain(join(binaries, 'letta'))
    expect(env).not.toHaveProperty('NODE_OPTIONS')
  })

  it('writes the provider records and the local backend mode, and returns the isolated variables', async () => {
    const env = await createLettaEnvironment(options())
    expect(env).toEqual({
      LETTA_HOME: join(runDirectory, 'home', '.letta'),
      LETTA_LOCAL_BACKEND_DIR: join(runDirectory, 'letta-backend'),
      LETTA_API_KEY: 'unit-key',
      LETTA_CODE_TELEM: '0',
      DO_NOT_TRACK: '1',
      LETTA_CODE_OFFLINE: '1',
      LETTA_DISABLE_MODS: '1',
      DISABLE_AUTOUPDATER: '1',
    })
    const auth = JSON.parse(readFileSync(join(env.LETTA_LOCAL_BACKEND_DIR!, 'providers', 'auth.json'), 'utf8'))
    expect(auth.providers['openai-compatible']).toEqual({ auth: { type: 'api', key: 'unit-key' }, base_url: 'http://127.0.0.1:4567/v1' })
    expect(auth.providers.openai).toMatchObject({ provider_type: 'openai', auth: { type: 'api', key: 'unit-key' }, base_url: 'http://127.0.0.1:4567/v1' })
    expect(JSON.parse(readFileSync(join(env.LETTA_HOME!, 'settings.json'), 'utf8'))).toEqual({ preferredBackendMode: 'local' })
  })

  it('runs no setup command when the search path holds no letta', async () => {
    await createLettaEnvironment(options())
    expect(setup.execute).not.toHaveBeenCalled()
  })

  it('runs both setup commands against the isolated store, with no proxy and the updater off', async () => {
    const binaries = join(runDirectory, 'binaries')
    mkdirSync(binaries)
    const letta = join(binaries, process.platform === 'win32' ? `letta${exeExtensionOnPath()}` : 'letta')
    writeFileSync(letta, 'controlled setup boundary', { mode: 0o755 })
    process.env.PATH = binaries
    const env = await createLettaEnvironment(options())
    expect(setup.execute.mock.calls.map(([file, args]) => [file, args])).toEqual([
      [letta, ['backend', 'local']],
      [letta, ['connect', 'openai-compatible', '--base-url', 'http://127.0.0.1:4567/v1', '--api-key', 'unit-key']],
    ])
    for (const [, , commandOptions] of setup.execute.mock.calls) {
      expect(commandOptions?.env).toMatchObject({ ...temporaryEnv, HOME: env.LETTA_HOME, DISABLE_AUTOUPDATER: '1', NO_PROXY: '127.0.0.1,localhost' })
      for (const key of ['HTTP_PROXY', 'HTTPS_PROXY', 'http_proxy', 'https_proxy', 'ALL_PROXY', 'all_proxy'])
        expect(commandOptions?.env, key).not.toHaveProperty(key)
    }
  })

  it('reports failed native setup without continuing to another command', async () => {
    const binaries = join(runDirectory, 'binaries')
    mkdirSync(binaries)
    writeFileSync(join(binaries, process.platform === 'win32' ? 'letta.exe' : 'letta'), 'controlled setup boundary', { mode: 0o755 })
    process.env.PATH = binaries
    setup.execute.mockImplementation(() => {
      throw new Error('The store is already prepared.')
    })
    await expect(Promise.resolve().then(() => createLettaEnvironment(options()))).rejects.toThrow('prepare Letta')
    expect(setup.execute).toHaveBeenCalledTimes(1)
  })
})
