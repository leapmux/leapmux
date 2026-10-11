import { Buffer } from 'node:buffer'
import { ChildProcess } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import process from 'node:process'
import { PassThrough } from 'node:stream'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { spawnRegisteredWorker, spawnWorkerProcess, withNativeWorker } from './nativeWorker'
import { createServerOutput } from './serverOutput'

const calls = vi.hoisted(() => ({
  mint: vi.fn(),
  list: vi.fn(),
  online: vi.fn(),
  deregister: vi.fn(),
  spawn: vi.fn(),
  stop: vi.fn(),
  directory: vi.fn(),
  environment: vi.fn(),
  alive: vi.fn(),
}))
vi.mock('./api', () => ({
  mintRegistrationKeyViaAPI: calls.mint,
  listOnlineWorkerIDsViaAPI: calls.list,
  waitForNewOnlineWorkerViaAPI: calls.online,
  deregisterWorkerViaAPI: calls.deregister,
}))
vi.mock('./processRegistry', () => ({ spawnTestProcess: calls.spawn }))
vi.mock('./process', () => ({ stopProcess: calls.stop }))
// The fake Worker holds a made-up process ID, which a real process on the host can hold also.
vi.mock('./processTree', () => ({ isAlive: calls.alive }))
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
  calls.alive.mockReturnValue(false)
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
      '--data-dir',
      directory,
      '--registration-key',
      'private-registration-key',
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
    await expect(withNativeWorker(server, {
      ...options,
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
    expect(calls.alive).toHaveBeenCalledWith(proc.pid)
    expect(onlineSignal().aborted).toBe(true)
  })

  it('does not start a Worker when registration fails', async () => {
    calls.mint.mockRejectedValue(new Error('Registration refused.'))
    await expect(withNativeWorker(server, options, async () => {})).rejects.toMatchObject({
      message: expect.stringContaining('The private Worker failed'),
      cause: { message: 'Registration refused.' },
    })
    expect(calls.spawn).not.toHaveBeenCalled()
    expect(calls.stop).not.toHaveBeenCalled()
    expect(existsSync(directory), 'the private data directory goes away with the failed registration').toBe(false)
  })

  it('fails the scenario when the Worker exits during it, and removes the listeners', async () => {
    let scenarioEntered!: () => void
    const entered = new Promise<void>((resolve) => {
      scenarioEntered = resolve
    })
    const operation = withNativeWorker(server, options, async () => {
      scenarioEntered()
      await new Promise(() => {})
    }).then(() => undefined, error => error)
    await entered
    Object.assign(proc, { exitCode: 3 })
    proc.emit('exit', 3, null)
    await expect(operation).resolves.toMatchObject({ cause: { message: 'The private Worker exited: code 3, signal null.' } })
    expect(calls.deregister).toHaveBeenCalledWith(server.hubUrl, server.adminToken, 'private-worker')
    expect(proc.listenerCount('error')).toBe(0)
    expect(proc.listenerCount('exit')).toBe(0)
    expect(existsSync(directory)).toBe(false)
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

  it('checks the process ID after the stop, on success and on a registration failure', async () => {
    calls.alive.mockImplementation((pid: number) => {
      expect(pid).toBe(proc.pid)
      expect(calls.stop).toHaveBeenCalledWith(proc)
      expect(proc.exitCode).toBe(0)
      return false
    })
    await withNativeWorker(server, options, async () => {})
    expect(calls.alive).toHaveBeenCalled()
    calls.alive.mockClear()
    calls.online.mockRejectedValueOnce(new Error('The next Worker did not reach online state.'))
    mkdirSync(directory)
    proc = Object.assign(new ChildProcess(), { stdout: new PassThrough(), stderr: new PassThrough(), pid: 31235 })
    calls.spawn.mockReturnValue(proc)
    await expect(withNativeWorker(server, options, async () => {})).rejects.toThrow('The private Worker failed')
    expect(calls.alive).toHaveBeenCalledWith(31235)
  })

  it('fails a stopped Worker whose process ID still exists, still deregisters it, and keeps its files', async () => {
    calls.alive.mockReturnValue(true)
    const privateDirectory = mkdtempSync(join(scratchRoot, 'native-worker-private-'))
    try {
      await expect(withNativeWorker(server, { ...options, privateDirectories: [privateDirectory] }, async () => {}))
        .rejects
        .toMatchObject({ cause: { errors: [{ message: 'The private Worker 31234 did not physically exit.' }] } })
      expect(calls.deregister).toHaveBeenCalledWith(server.hubUrl, server.adminToken, 'private-worker')
      expect(existsSync(directory), 'a Worker that still runs keeps its data directory').toBe(true)
      expect(existsSync(privateDirectory), 'a Worker that still runs keeps its private files').toBe(true)
    }
    finally {
      rmSync(privateDirectory, { recursive: true, force: true })
    }
  })

  it('does not check the process ID when the stop itself fails', async () => {
    calls.stop.mockRejectedValueOnce(new Error('The private Worker did not stop.'))
    await expect(withNativeWorker(server, options, async () => {})).rejects.toThrow('The private Worker failed')
    expect(calls.alive).not.toHaveBeenCalled()
    expect(calls.deregister).toHaveBeenCalledOnce()
  })

  it('removes its private directories with its data directory after the Worker exits', async () => {
    const privateDirectory = mkdtempSync(join(scratchRoot, 'native-worker-private-'))
    await withNativeWorker(server, { ...options, privateDirectories: [privateDirectory] }, async () => {
      expect(existsSync(privateDirectory)).toBe(true)
    })
    expect(existsSync(directory)).toBe(false)
    expect(existsSync(privateDirectory)).toBe(false)
  })

  it('removes its private directories when no Worker process started', async () => {
    const privateDirectory = mkdtempSync(join(scratchRoot, 'native-worker-private-'))
    calls.mint.mockRejectedValue(new Error('Registration refused.'))
    await expect(withNativeWorker(server, { ...options, privateDirectories: [privateDirectory] }, async () => {})).rejects.toThrow('The private Worker failed')
    expect(existsSync(privateDirectory)).toBe(false)
  })

  it('keeps its private directories after a failed stop, because the Worker can still read them', async () => {
    const privateDirectory = mkdtempSync(join(scratchRoot, 'native-worker-private-'))
    try {
      calls.stop.mockRejectedValue(new Error('The private Worker did not stop.'))
      await expect(withNativeWorker(server, { ...options, privateDirectories: [privateDirectory] }, async () => {})).rejects.toThrow('The private Worker failed')
      expect(existsSync(privateDirectory)).toBe(true)
    }
    finally {
      rmSync(privateDirectory, { recursive: true, force: true })
    }
  })

  it('removes its private directories when it refuses its options before a Worker starts', async () => {
    const privateDirectory = mkdtempSync(join(scratchRoot, 'native-worker-private-'))
    await expect(withNativeWorker({ ...server, agentEnv: {} }, { ...options, privateDirectories: [privateDirectory] }, async () => {})).rejects.toThrow('isolated agent environment')
    expect(existsSync(privateDirectory)).toBe(false)
    expect(calls.directory).not.toHaveBeenCalled()
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

describe('spawnWorkerProcess', () => {
  it('starts the Worker for the hub and the data directory, with the name in the environment and the output label', () => {
    const output = createServerOutput()
    expect(spawnWorkerProcess({ hubUrl: server.hubUrl, name: 'restart-worker', dataDir: directory, env: { HOME: '/private/home' }, output })).toBe(proc)
    expect(calls.spawn).toHaveBeenCalledWith('/isolated/leapmux', ['worker', '--hub', server.hubUrl, '--data-dir', directory], {
      stdio: ['ignore', 'pipe', 'pipe'],
      detached: false,
      env: { HOME: '/private/home', LEAPMUX_WORKER_NAME: 'restart-worker', PRIVATE_ENV_FILTERED: 'true' },
    })
    proc.stdout?.emit('data', Buffer.from('connected\n'))
    expect(output.since(0)).toBe('[restart-worker] connected')
  })

  it('appends the extra arguments, and unreferences a detached Worker', () => {
    const unref = vi.spyOn(proc, 'unref')
    spawnWorkerProcess({ hubUrl: server.hubUrl, name: 'detached-worker', dataDir: directory, extraArgs: ['--encryption-mode', 'post-quantum'], output: createServerOutput(), detached: true })
    expect(calls.spawn).toHaveBeenCalledWith('/isolated/leapmux', ['worker', '--hub', server.hubUrl, '--data-dir', directory, '--encryption-mode', 'post-quantum'], expect.objectContaining({ detached: true }))
    expect(unref).toHaveBeenCalledOnce()
  })

  it('keeps an attached Worker referenced', () => {
    const unref = vi.spyOn(proc, 'unref')
    spawnWorkerProcess({ hubUrl: server.hubUrl, name: 'attached-worker', dataDir: directory, output: createServerOutput() })
    expect(unref).not.toHaveBeenCalled()
  })
})

describe('spawnRegisteredWorker', () => {
  const hub = { hubUrl: server.hubUrl, adminToken: server.adminToken }

  it('registers the Worker with a fresh key, gives the process to onSpawn before the online wait, and returns its ID', async () => {
    const onSpawn = vi.fn()
    const registered = await spawnRegisteredWorker(hub, { name: 'registered-worker', dataDir: directory, extraArgs: ['--encryption-mode', 'post-quantum'], output: createServerOutput(), onSpawn })
    expect(registered).toEqual({ proc, workerId: 'private-worker' })
    expect(calls.spawn).toHaveBeenCalledWith('/isolated/leapmux', [
      'worker',
      '--hub',
      server.hubUrl,
      '--data-dir',
      directory,
      '--registration-key',
      'private-registration-key',
      '--encryption-mode',
      'post-quantum',
    ], expect.objectContaining({ env: expect.objectContaining({ LEAPMUX_WORKER_NAME: 'registered-worker' }) }))
    expect(onSpawn).toHaveBeenCalledExactlyOnceWith(proc)
    expect(onSpawn.mock.invocationCallOrder[0]).toBeLessThan(calls.online.mock.invocationCallOrder[0]!)
    expect(calls.list.mock.invocationCallOrder[0]).toBeLessThan(calls.spawn.mock.invocationCallOrder[0]!)
    expect(calls.online).toHaveBeenCalledWith(server.hubUrl, server.adminToken, new Set(['suite-worker']), undefined, expect.any(AbortSignal))
    expect(onlineSignal().aborted).toBe(true)
    expect(calls.stop).not.toHaveBeenCalled()
    expect(proc.listenerCount('error')).toBe(0)
    expect(proc.listenerCount('exit')).toBe(0)
  })

  it('fails at once when the Worker exits during the online wait, and stops it', async () => {
    calls.online.mockImplementation(() => new Promise<string>(() => {}))
    const registration = spawnRegisteredWorker(hub, { name: 'exiting-worker', dataDir: directory, output: createServerOutput() })
    await vi.waitFor(() => expect(calls.online).toHaveBeenCalled())
    Object.assign(proc, { exitCode: 2 })
    proc.emit('exit', 2, null)
    await expect(registration).rejects.toThrow('The Worker exiting-worker exited: code 2, signal null.')
    expect(onlineSignal().aborted).toBe(true)
    expect(calls.stop).toHaveBeenCalledExactlyOnceWith(proc)
    expect(proc.listenerCount('exit')).toBe(0)
  })

  it('refuses a Worker that exited before the online wait', async () => {
    Object.assign(proc, { exitCode: 1 })
    await expect(spawnRegisteredWorker(hub, { name: 'finished-worker', dataDir: directory, output: createServerOutput() }))
      .rejects
      .toThrow('The Worker finished-worker exited already: code 1, signal null.')
    expect(calls.online).not.toHaveBeenCalled()
    expect(calls.stop).toHaveBeenCalledExactlyOnceWith(proc)
  })

  it('stops the Worker when onSpawn fails', async () => {
    const failure = new Error('The caller could not track the Worker.')
    await expect(spawnRegisteredWorker(hub, { name: 'untracked-worker', dataDir: directory, output: createServerOutput(), onSpawn: () => {
      throw failure
    } })).rejects.toBe(failure)
    expect(calls.online).not.toHaveBeenCalled()
    expect(calls.stop).toHaveBeenCalledExactlyOnceWith(proc)
  })

  it('reports the registration failure and the stop failure together', async () => {
    const registrationFailure = new Error('No new Worker reached online state.')
    const stopFailure = new Error('The Worker did not stop.')
    calls.online.mockRejectedValue(registrationFailure)
    calls.stop.mockRejectedValue(stopFailure)
    await expect(spawnRegisteredWorker(hub, { name: 'stuck-worker', dataDir: directory, output: createServerOutput() }))
      .rejects
      .toMatchObject({ errors: [registrationFailure, stopFailure] })
  })

  it('spawns nothing when the hub refuses the registration key', async () => {
    calls.mint.mockRejectedValue(new Error('Registration refused.'))
    await expect(spawnRegisteredWorker(hub, { name: 'refused-worker', dataDir: directory, output: createServerOutput() })).rejects.toThrow('Registration refused.')
    expect(calls.spawn).not.toHaveBeenCalled()
    expect(calls.stop).not.toHaveBeenCalled()
  })
})

function onlineSignal(): AbortSignal {
  const signal: unknown = calls.online.mock.calls[0]?.[4]
  if (!(signal instanceof AbortSignal))
    throw new Error('The private Worker online wait received no AbortSignal.')
  return signal
}
