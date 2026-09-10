import type { SeparateServerInfo } from './process-control-fixtures'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createProcessStub } from '~/test-support/childProcess'
import { listOnlineWorkerIDsViaAPI, loginViaAPI } from './helpers/api'
import { stopProcess } from './helpers/process'
import { spawnTestProcess } from './helpers/processRegistry'
import { waitForServer } from './helpers/server'
import { reportStartupFailure } from './helpers/serverOutput'
import { restartHub, restartWorker } from './process-control-fixtures'

vi.mock('./helpers/api', () => ({
  API_POLL_INTERVAL_MS: 150,
  listOnlineWorkerIDsViaAPI: vi.fn(),
  loginViaAPI: vi.fn(),
  TEST_ADMIN_USERNAME: 'admin',
  TEST_ADMIN_PASSWORD: 'password',
}))
vi.mock('./helpers/crdt', () => ({ closeAllUserEventsSubscriptions: vi.fn() }))
vi.mock('./helpers/process', () => ({ stopProcess: vi.fn() }))
vi.mock('./helpers/processRegistry', () => ({ spawnTestProcess: vi.fn() }))
vi.mock('./helpers/server', () => ({ waitForServer: vi.fn(), hubSpawnEnv: (env: unknown) => env }))
vi.mock('./helpers/serverOutput', () => ({
  reportStartupFailure: vi.fn((_output: unknown, _what: string, error: unknown): never => { throw error }),
}))
vi.mock('./helpers/ui', () => ({}))
vi.mock('./realAgentSettings', () => ({ realAgentEnv: () => ({}) }))

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
