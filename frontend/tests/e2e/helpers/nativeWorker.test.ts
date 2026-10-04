import { Buffer } from 'node:buffer'
import { ChildProcess } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { PassThrough } from 'node:stream'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { withNativeWorker } from './nativeWorker'

const calls = vi.hoisted(() => ({
  mint: vi.fn(),
  list: vi.fn(),
  online: vi.fn(),
  deregister: vi.fn(),
  spawn: vi.fn(),
  stop: vi.fn(),
  directory: vi.fn(),
  environment: vi.fn(),
}))
vi.mock('./api', () => ({
  mintRegistrationKeyViaAPI: calls.mint,
  listOnlineWorkerIDsViaAPI: calls.list,
  waitForNewOnlineWorkerViaAPI: calls.online,
  deregisterWorkerViaAPI: calls.deregister,
}))
vi.mock('./processRegistry', () => ({ spawnTestProcess: calls.spawn }))
vi.mock('./process', () => ({ stopProcess: calls.stop }))
vi.mock('./runDirectory', () => ({ createTestDirectory: calls.directory }))
vi.mock('./server', () => ({
  getGlobalState: () => ({ binaryPath: '/isolated/leapmux' }),
  hubSpawnEnv: calls.environment,
}))

const scratchRoot = resolve(process.cwd(), '../.tmp')
const server = { hubUrl: 'http://localhost:32100', adminToken: 'private-token', workerId: 'suite-worker', agentEnv: { HOME: '/private/native-home', MODEL_TOKEN: 'mock-token' } }
const options = { dataDirPrefix: 'native-worker-unit', workerName: 'Private unit Worker' }
let directory: string
let proc: ChildProcess

beforeEach(() => {
  vi.resetAllMocks()
  mkdirSync(scratchRoot, { recursive: true })
  directory = mkdtempSync(join(scratchRoot, 'native-worker-unit-'))
  proc = Object.assign(new ChildProcess(), { stdout: new PassThrough(), stderr: new PassThrough(), pid: 31234 })
  calls.mint.mockResolvedValue('private-registration-key')
  calls.list.mockResolvedValue(['suite-worker'])
  calls.online.mockResolvedValue('private-worker')
  calls.deregister.mockResolvedValue(undefined)
  calls.directory.mockReturnValue(directory)
  calls.spawn.mockReturnValue(proc)
  calls.environment.mockImplementation(value => ({ ...value, PRIVATE_ENV_FILTERED: 'true' }))
  calls.stop.mockImplementation(async () => {
    Object.assign(proc, { exitCode: 0 })
    proc.emit('close', 0)
  })
})
afterEach(() => rmSync(directory, { recursive: true, force: true }))

describe('withNativeWorker', () => {
  it('captures an asynchronous spawn error before online registration', async () => {
    let entered!: () => void
    let cancelOnline!: (error: Error) => void
    const onlineStarted = new Promise<void>((resolve) => {
      entered = resolve
    })
    calls.online.mockImplementation(() => {
      entered()
      return new Promise<string>((_, reject) => {
        cancelOnline = reject
      })
    })
    const operation = withNativeWorker(server, options, async () => {})
      .then(() => undefined, error => error)
    const spawnError = new Error('The native Worker failed to spawn asynchronously.')
    try {
      await onlineStarted
      expect(() => proc.emit('error', spawnError)).not.toThrow()
      await expect(operation).resolves.toMatchObject({ cause: spawnError })
      expect(calls.stop).toHaveBeenCalledWith(proc)
      expect(calls.deregister).not.toHaveBeenCalled()
      expect(onlineSignal().aborted).toBe(true)
      expect(proc.listenerCount('error')).toBe(0)
      expect(proc.listenerCount('exit')).toBe(0)
    }
    finally {
      cancelOnline(spawnError)
      await operation
    }
  })

  it('uses private native settings and returns the registered Worker identity', async () => {
    await withNativeWorker(server, { ...options, env: { PRIVATE_MODEL: 'mock', REMOVED_VALUE: undefined } }, async (worker) => {
      expect(worker.server.workerId).toBe('private-worker')
      expect(worker.workerId).toBe('private-worker')
      expect(worker.server.agentEnv).toEqual({ ...server.agentEnv, PRIVATE_MODEL: 'mock' })
      expect(worker.dataDir).toBe(directory)
      expect(existsSync(directory)).toBe(true)
    })
    expect(calls.environment).toHaveBeenCalledWith({ ...server.agentEnv, PRIVATE_MODEL: 'mock', REMOVED_VALUE: undefined, LEAPMUX_WORKER_NAME: options.workerName })
    expect(calls.spawn).toHaveBeenCalledWith('/isolated/leapmux', [
      'worker',
      '--hub',
      server.hubUrl,
      '--registration-key',
      'private-registration-key',
      '--data-dir',
      directory,
    ], expect.objectContaining({ env: expect.objectContaining({ HOME: server.agentEnv.HOME, PRIVATE_ENV_FILTERED: 'true' }) }))
    expect(calls.online).toHaveBeenCalledWith(server.hubUrl, server.adminToken, new Set(['suite-worker']), undefined, expect.any(AbortSignal))
    expect(onlineSignal().aborted).toBe(true)
    expect(proc.listenerCount('error')).toBe(0)
    expect(proc.listenerCount('exit')).toBe(0)
    expect(calls.stop).toHaveBeenCalledWith(proc)
    expect(calls.deregister).toHaveBeenCalledWith(server.hubUrl, server.adminToken, 'private-worker')
    expect(existsSync(directory)).toBe(false)
  })

  it('keeps independent stream callbacks and captured failure output', async () => {
    const chunks: Array<{ text: string, stream: string }> = []
    const ends = new Set<string>()
    const afterStop = vi.fn()
    await expect(withNativeWorker(server, {
      ...options,
      afterStop,
      onStdio: (chunk, stream) => { chunks.push({ text: chunk.toString(), stream }) },
      onStdioEnd: (stream) => { ends.add(stream) },
    }, async () => {
      proc.stdout?.emit('data', Buffer.from('stdout-part'))
      proc.stderr?.emit('data', Buffer.from('stderr-part'))
      throw new Error('The native test failed.')
    })).rejects.toThrow(/stdout-part\n.*stderr-part/)
    expect(chunks).toEqual([{ text: 'stdout-part', stream: 'stdout' }, { text: 'stderr-part', stream: 'stderr' }])
    expect(ends).toEqual(new Set(['stdout', 'stderr']))
    expect(calls.stop).toHaveBeenCalledOnce()
    expect(calls.deregister).toHaveBeenCalledOnce()
    expect(afterStop).toHaveBeenCalledWith(proc)
    expect(onlineSignal().aborted).toBe(true)
  })

  it('does not start a Worker when registration fails', async () => {
    calls.mint.mockRejectedValue(new Error('Registration refused.'))
    await expect(withNativeWorker(server, options, async () => {})).rejects.toThrow('Registration refused')
    expect(calls.directory).not.toHaveBeenCalled()
    expect(calls.spawn).not.toHaveBeenCalled()
    expect(calls.stop).not.toHaveBeenCalled()
  })

  it('stops a Worker whose registration never reaches online state', async () => {
    calls.online.mockRejectedValue(new Error('No new Worker reached online state.'))
    await expect(withNativeWorker(server, options, async () => {})).rejects.toThrow('The private Worker failed')
    expect(calls.stop).toHaveBeenCalledWith(proc)
    expect(calls.deregister).not.toHaveBeenCalled()
    expect(existsSync(directory)).toBe(false)
    expect(onlineSignal().aborted).toBe(true)
  })

  it('removes its private data directory when process creation throws', async () => {
    calls.spawn.mockImplementation(() => {
      throw new Error('Process creation failed.')
    })
    await expect(withNativeWorker(server, options, async () => {})).rejects.toThrow('The private Worker failed')
    expect(existsSync(directory)).toBe(false)
    expect(calls.stop).not.toHaveBeenCalled()
  })

  it('rejects an already-finished Worker before accepting a registration identity', async () => {
    Object.assign(proc, { exitCode: 1 })
    const use = vi.fn(async () => {})
    await expect(withNativeWorker(server, options, use)).rejects.toThrow('The private Worker failed')
    expect(use).not.toHaveBeenCalled()
    expect(calls.deregister).not.toHaveBeenCalled()
  })

  it('attempts stop and deregistration even when both cleanup operations fail', async () => {
    const stopError = new Error('Stop failed.')
    const deregisterError = new Error('Deregistration failed.')
    calls.stop.mockRejectedValue(stopError)
    calls.deregister.mockRejectedValue(deregisterError)
    await expect(withNativeWorker(server, options, async () => {})).rejects.toMatchObject({
      cause: { errors: [stopError, deregisterError] },
    })
    expect(calls.stop).toHaveBeenCalledOnce()
    expect(calls.deregister).toHaveBeenCalledOnce()
    expect(existsSync(directory), 'a live Worker keeps its files after stop fails').toBe(true)
  })

  it('awaits the physical process check after stop on success and registration failure', async () => {
    const afterStop = vi.fn(async (stopped: ChildProcess) => {
      expect(stopped).toBe(proc)
      expect(calls.stop).toHaveBeenCalledWith(stopped)
      expect(stopped.exitCode).toBe(0)
    })
    await withNativeWorker(server, { ...options, afterStop }, async () => {})
    expect(afterStop).toHaveBeenCalledOnce()
    calls.online.mockRejectedValueOnce(new Error('The next Worker did not reach online state.'))
    mkdirSync(directory)
    proc = Object.assign(new ChildProcess(), { stdout: new PassThrough(), stderr: new PassThrough(), pid: 31235 })
    calls.spawn.mockReturnValue(proc)
    await expect(withNativeWorker(server, { ...options, afterStop }, async () => {})).rejects.toThrow('The private Worker failed')
    expect(afterStop).toHaveBeenCalledTimes(2)
  })

  it('still deregisters the Worker when its physical process check fails', async () => {
    const failure = new Error('The private Worker PID still exists.')
    const afterStop = vi.fn(async () => {
      throw failure
    })
    await expect(withNativeWorker(server, { ...options, afterStop }, async () => {})).rejects.toMatchObject({ cause: { errors: [failure] } })
    expect(afterStop).toHaveBeenCalledWith(proc)
    expect(calls.deregister).toHaveBeenCalledWith(server.hubUrl, server.adminToken, 'private-worker')
  })

  it('does not report physical stop when stop itself fails', async () => {
    const afterStop = vi.fn()
    calls.stop.mockRejectedValueOnce(new Error('The private Worker did not stop.'))
    await expect(withNativeWorker(server, { ...options, afterStop }, async () => {})).rejects.toThrow('The private Worker failed')
    expect(afterStop).not.toHaveBeenCalled()
    expect(calls.deregister).toHaveBeenCalledOnce()
  })

  it('rejects absent or empty private homes before registration', async () => {
    const withoutAgentEnv = { hubUrl: server.hubUrl, adminToken: server.adminToken, workerId: server.workerId }
    for (const value of [withoutAgentEnv, { ...server, agentEnv: {} }, { ...server, agentEnv: { HOME: '' } }])
      await expect(withNativeWorker(value, options, async () => {})).rejects.toThrow('isolated agent environment')
    for (const HOME of ['', ' ', undefined])
      await expect(withNativeWorker(server, { ...options, env: { HOME } }, async () => {})).rejects.toThrow('nonempty isolated HOME')
    expect(calls.mint).not.toHaveBeenCalled()
  })
})

function onlineSignal(): AbortSignal {
  const signal: unknown = calls.online.mock.calls[0]?.[4]
  if (!(signal instanceof AbortSignal))
    throw new Error('The private Worker online wait received no AbortSignal.')
  return signal
}
