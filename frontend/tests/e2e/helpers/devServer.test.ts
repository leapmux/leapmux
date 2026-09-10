import { mkdirSync, mkdtempSync, readdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createProcessStub } from '~/test-support/childProcess'
import { closeTestChannels, getUserId, getWorkerId, signUpViaAPI } from './api'
import { startDevServer, startSoloServer, stopDevServer } from './devServer'
import { spawnTestProcess } from './processRegistry'
import { waitForServer } from './server'

let root: string

vi.mock('./api', () => ({
  closeTestChannels: vi.fn(async () => {}),
  getUserId: vi.fn(),
  getWorkerId: vi.fn(),
  signUpViaAPI: vi.fn(),
  TEST_ADMIN_DISPLAY_NAME: 'Admin',
  TEST_ADMIN_PASSWORD: 'password',
  TEST_ADMIN_USERNAME: 'admin',
}))
vi.mock('./processRegistry', () => ({ spawnTestProcess: vi.fn() }))
vi.mock('./server', () => ({
  getGlobalState: () => ({ binaryPath: 'leapmux', tmpDir: root }),
  findFreePort: async () => 12345,
  hubSpawnEnv: (env: unknown) => env,
  waitForServer: vi.fn(),
}))

beforeEach(() => {
  const scratch = resolve(import.meta.dirname, '../../../..', '.tmp')
  mkdirSync(scratch, { recursive: true })
  root = mkdtempSync(join(scratch, 'dev-server-test-'))
  vi.mocked(waitForServer).mockReset().mockResolvedValue(undefined)
  vi.mocked(signUpViaAPI).mockReset().mockResolvedValue('session')
  vi.mocked(getUserId).mockReset().mockResolvedValue('user')
  vi.mocked(getWorkerId).mockReset().mockResolvedValue('worker')
  vi.mocked(closeTestChannels).mockClear()
})

afterEach(() => rmSync(root, { recursive: true, force: true }))

function child() {
  const stub = createProcessStub()
  stub.emitter.kill.mockImplementation(() => {
    stub.emitter.exitCode = 0
    stub.emitter.emit('exit', 0, null)
    return true
  })
  vi.mocked(spawnTestProcess).mockReturnValue(stub.proc)
  return stub
}

describe('private server lifetime', () => {
  it.each(['readiness', 'signup', 'user', 'worker'])('cleans a dev process and its directory after a failed %s step', async (stage) => {
    const stub = child()
    const error = new Error(`${stage} failed`)
    const operation = { readiness: waitForServer, signup: signUpViaAPI, user: getUserId, worker: getWorkerId }[stage]
    vi.mocked(operation!).mockRejectedValueOnce(error)
    await expect(startDevServer()).rejects.toBe(error)
    expect(stub.emitter.kill).toHaveBeenCalledExactlyOnceWith('SIGTERM')
    expect(readdirSync(root)).toEqual([])
  })

  it('transfers a ready process to its caller and closes it on request', async () => {
    const stub = child()
    const handle = await startDevServer()
    expect(stub.emitter.kill).not.toHaveBeenCalled()
    expect(readdirSync(root)).toHaveLength(1)
    await stopDevServer(handle)
    expect(closeTestChannels).toHaveBeenCalledExactlyOnceWith(handle.hubUrl)
    expect(stub.emitter.kill).toHaveBeenCalledExactlyOnceWith('SIGTERM')
    expect(readdirSync(root)).toEqual([])
  })

  it('preserves cleanup after solo readiness fails', async () => {
    const stub = child()
    const error = new Error('solo not ready')
    vi.mocked(waitForServer).mockRejectedValueOnce(error)
    await expect(startSoloServer()).rejects.toBe(error)
    expect(stub.emitter.kill).toHaveBeenCalledExactlyOnceWith('SIGTERM')
    expect(readdirSync(root)).toEqual([])
  })
})
