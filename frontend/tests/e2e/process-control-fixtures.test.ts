import type { SeparateServerInfo } from './process-control-fixtures'
import { mkdirSync, mkdtempSync, readdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createProcessStub } from '~/test-support/childProcess'
import { listOnlineWorkerIDsViaAPI, loginViaAPI, waitForNewOnlineWorkerViaAPI } from './helpers/api'
import { modelScriptFixtures } from './helpers/modelScriptFixture'
import { stopProcess, stopProcesses } from './helpers/process'
import { spawnTestProcess } from './helpers/processRegistry'
import { findFreePort, waitForHubReady, waitForHubStateFile, waitForServer } from './helpers/server'
import { reportStartupFailure } from './helpers/serverOutput'
import { restartHub, restartWorker } from './process-control-fixtures'

const startup = vi.hoisted(() => ({ root: '', fixtures: new Map<string, unknown>() }))

vi.mock('@playwright/test', async (original) => {
  const actual = await original<typeof import('@playwright/test')>()
  const test = new Proxy(actual.test, {
    get: (target, property, receiver) => property === 'extend'
      ? (fixtures: Record<string, unknown>) => {
          for (const [name, fixture] of Object.entries(fixtures))
            startup.fixtures.set(name, fixture)
          return target
        }
      : Reflect.get(target, property, receiver),
  })
  return { ...actual, test }
})

vi.mock('./helpers/api', () => ({
  API_POLL_INTERVAL_MS: 150,
  listOnlineWorkerIDsViaAPI: vi.fn(),
  loginViaAPI: vi.fn(),
  signUpViaAPI: vi.fn(async () => 'private-session'),
  elevateSessionViaAPI: vi.fn(async () => {}),
  enableSignupViaAPI: vi.fn(async () => {}),
  mintRegistrationKeyViaAPI: vi.fn(async () => 'private-registration'),
  waitForNewOnlineWorkerViaAPI: vi.fn(),
  closeTestChannels: vi.fn(async () => {}),
  TEST_ADMIN_USERNAME: 'admin',
  TEST_ADMIN_PASSWORD: 'password',
  TEST_ADMIN_DISPLAY_NAME: 'Administrator',
}))
vi.mock('./helpers/crdt', () => ({ closeAllUserEventsSubscriptions: vi.fn() }))
vi.mock('./helpers/process', () => ({ stopProcess: vi.fn(), stopProcesses: vi.fn(async () => {}) }))
vi.mock('./helpers/processRegistry', () => ({ spawnTestProcess: vi.fn() }))
vi.mock('./helpers/server', async original => ({
  ...await original<typeof import('./helpers/server')>(),
  getGlobalState: () => ({ binaryPath: 'private-leapmux', tmpDir: startup.root }),
  findFreePort: vi.fn(async () => 12345),
  waitForServer: vi.fn(),
  waitForHubStateFile: vi.fn(),
  waitForHubReady: vi.fn(),
  hubSpawnEnv: (env: unknown) => env,
}))
vi.mock('./helpers/serverOutput', async original => ({
  ...await original<typeof import('./helpers/serverOutput')>(),
  reportStartupFailure: vi.fn((_output: unknown, _what: string, error: unknown): never => { throw error }),
}))
vi.mock('./helpers/ui', () => ({}))
vi.mock('./agentSettings', () => ({ agentDefaultsEnv: () => ({}) }))

let server: SeparateServerInfo
let replacement: ReturnType<typeof createProcessStub>['proc']

beforeEach(() => {
  vi.resetAllMocks()
  replacement = Object.assign(createProcessStub({ pid: 456 }).proc, { unref: vi.fn() })
  server = {
    hubUrl: 'http://localhost:12345',
    adminToken: 'session',
    workerId: 'worker',
    newuserToken: 'other-session',
    hubProc: createProcessStub({ pid: 123 }).proc,
    workerProc: createProcessStub({ pid: 124 }).proc,
    dataDir: '/test-data',
    binaryPath: 'leapmux',
    hubPort: 12345,
    output: { mark: () => 0, since: () => '', capture: vi.fn() },
  }
  vi.mocked(stopProcess).mockResolvedValue(undefined)
  vi.mocked(spawnTestProcess).mockReturnValue(replacement)
  vi.mocked(waitForServer).mockResolvedValue(undefined)
  vi.mocked(loginViaAPI).mockResolvedValue('new-session')
  vi.mocked(listOnlineWorkerIDsViaAPI).mockResolvedValueOnce([]).mockResolvedValue(['worker'])
})

describe('processTest', () => {
  it('registers the model script and the test deadline that the suite test base also uses', () => {
    expect(startup.fixtures.get('testStartedAt')).toBe(modelScriptFixtures.testStartedAt)
    expect(startup.fixtures.get('modelScript')).toBe(modelScriptFixtures.modelScript)
  })
})

describe('process restart cleanup', () => {
  it.each([
    { label: 'hub', restart: restartHub, field: 'hubProc' as const },
    { label: 'worker', restart: restartWorker, field: 'workerProc' as const },
  ])('retains the ready replacement $label for fixture teardown', async ({ restart, field }) => {
    const previous = server[field]
    await restart(server)
    expect(stopProcess).toHaveBeenCalledExactlyOnceWith(previous)
    expect(server[field]).toBe(replacement)
    expect(server.output.capture).toHaveBeenCalledWith(replacement, field === 'hubProc' ? 'hub' : 'worker')
    expect(reportStartupFailure).not.toHaveBeenCalled()
    if (field === 'hubProc')
      expect(loginViaAPI).toHaveBeenCalledWith(server.hubUrl, 'admin', 'password')
    else
      expect(listOnlineWorkerIDsViaAPI).toHaveBeenCalledTimes(2)
  })

  describe.each(['hub readiness', 'hub login', 'worker registration'])('%s failure', (stage) => {
    it.each([false, true])('preserves the startup error when cleanup also fails: %s', async (cleanupFails) => {
      const startupError = new Error(`${stage} failed`)
      const cleanupError = new Error('Cannot stop the replacement')
      if (stage === 'hub readiness')
        vi.mocked(waitForServer).mockRejectedValueOnce(startupError)
      else if (stage === 'hub login')
        vi.mocked(loginViaAPI).mockRejectedValueOnce(startupError)
      else
        vi.mocked(listOnlineWorkerIDsViaAPI).mockReset().mockResolvedValueOnce([]).mockRejectedValueOnce(startupError)
      if (cleanupFails)
        vi.mocked(stopProcess).mockResolvedValueOnce(undefined).mockRejectedValueOnce(cleanupError)
      const restart = stage.startsWith('hub') ? restartHub : restartWorker
      const result = await restart(server).then(() => null, error => error)
      if (cleanupFails) {
        expect(result).toBeInstanceOf(AggregateError)
        expect(result.errors).toEqual([startupError, cleanupError])
      }
      else {
        expect(result).toBe(startupError)
      }
      expect(stopProcess).toHaveBeenCalledTimes(2)
      expect(stopProcess).toHaveBeenLastCalledWith(replacement)
      expect(reportStartupFailure).toHaveBeenCalledWith(server.output, expect.any(String), result)
    })
  })
})

async function runInitialFixture(use: (server: SeparateServerInfo) => Promise<void>): Promise<void> {
  const fixture = startup.fixtures.get('separateHubWorker')
  const run = Array.isArray(fixture) ? fixture[0] : undefined
  if (typeof run !== 'function')
    throw new Error('The separate Hub and Worker fixture callback is absent.')
  await run({}, use)
}

afterEach(() => {
  if (startup.root) {
    rmSync(startup.root, { recursive: true, force: true })
    startup.root = ''
  }
})

describe('initial process fixture port ownership', () => {
  let children: ReturnType<typeof createProcessStub>[]
  beforeEach(() => {
    const scratch = resolve(import.meta.dirname, '../../..', '.tmp')
    mkdirSync(scratch, { recursive: true })
    startup.root = mkdtempSync(join(scratch, 'process-fixture-startup-'))
    children = []
    vi.mocked(findFreePort).mockResolvedValue(12345)
    vi.mocked(waitForHubStateFile).mockResolvedValue(JSON.stringify({ listen: ['127.0.0.1:24680'] }))
    vi.mocked(waitForHubReady).mockResolvedValue(undefined)
    vi.mocked(waitForNewOnlineWorkerViaAPI).mockResolvedValue('assigned-worker')
    vi.mocked(spawnTestProcess).mockImplementation((_command, _args) => {
      const stub = createProcessStub({ pid: 1000 + children.length })
      children.push(stub)
      return Object.assign(stub.proc, { unref: vi.fn() })
    })
    // Another process takes the old provisional port before the Hub can bind it.
    vi.mocked(waitForServer).mockImplementation(async (url) => {
      if (url === 'http://localhost:12345')
        throw new Error('The provisional port belongs to another shard.')
    })
  })

  it('starts the Hub on its assigned port while the old provisional port is occupied', async () => {
    const use = vi.fn<(server: SeparateServerInfo) => Promise<void>>(async () => {})
    await runInitialFixture(use)
    expect(findFreePort).not.toHaveBeenCalled()
    expect(spawnTestProcess).toHaveBeenNthCalledWith(1, 'private-leapmux', [
      'hub',
      '-listen',
      '127.0.0.1:0',
      '-data-dir',
      expect.any(String),
    ], expect.any(Object))
    expect(waitForHubStateFile).toHaveBeenCalledWith(expect.stringMatching(/state\.json$/), children[0]?.proc)
    expect(waitForHubReady).toHaveBeenCalledWith('http://localhost:24680', children[0]?.proc)
    expect(use).toHaveBeenCalledWith(expect.objectContaining({ hubUrl: 'http://localhost:24680', hubPort: 24680, workerId: 'assigned-worker' }))
    expect(spawnTestProcess).toHaveBeenNthCalledWith(2, 'private-leapmux', expect.arrayContaining(['--hub', 'http://localhost:24680']), expect.any(Object))
    expect(stopProcesses).toHaveBeenCalledWith([...children].reverse().map(child => child.proc))
    expect(readdirSync(startup.root)).toEqual([])
  })

  it.each(['state file', 'readiness', 'registration', 'spawn'])('cleans partial initial startup after %s fails', async (stage) => {
    const failed = new Error(`The controlled ${stage} failed.`)
    if (stage === 'state file') {
      vi.mocked(waitForHubStateFile).mockRejectedValueOnce(failed)
    }
    else if (stage === 'readiness') {
      vi.mocked(waitForHubReady).mockRejectedValueOnce(failed)
    }
    else if (stage === 'registration') {
      vi.mocked(waitForNewOnlineWorkerViaAPI).mockRejectedValueOnce(failed)
    }
    else {
      vi.mocked(spawnTestProcess).mockImplementationOnce(() => {
        throw failed
      })
    }
    await expect(runInitialFixture(async () => {})).rejects.toBe(failed)
    expect(stopProcesses).toHaveBeenCalledWith(children.map(child => child.proc))
    expect(readdirSync(startup.root)).toEqual([])
  })

  it('preserves the initial startup failure when process cleanup fails too', async () => {
    const failed = new Error('The controlled initial state file failed.')
    const cleanupFailed = new Error('The controlled initial process cleanup failed.')
    vi.mocked(waitForHubStateFile).mockRejectedValueOnce(failed)
    vi.mocked(stopProcesses).mockRejectedValueOnce(cleanupFailed)
    const result: unknown = await runInitialFixture(async () => {}).then(() => null, (error: unknown) => error)
    expect(result).toBeInstanceOf(AggregateError)
    if (!(result instanceof AggregateError))
      throw new Error('The initial fixture did not preserve both failures.')
    expect(result.errors[0]).toBe(failed)
    expect(result.errors[1]).toMatchObject({ errors: [cleanupFailed] })
  })
})
