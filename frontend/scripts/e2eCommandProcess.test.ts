import { spawn } from 'node:child_process'
import { once } from 'node:events'
import process from 'node:process'
import { PassThrough } from 'node:stream'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createProcessStub } from '~/test-support/childProcess'
import { spawnCommandProcess } from './e2eCommandProcess'
import { spawnWindowsCommandJob } from './windowsCommandJob'

const kernel = vi.hoisted(() => ({
  alive: new Set<number>(),
  failure: undefined as Error | undefined,
}))

vi.mock('node:process', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:process')>()
  return {
    ...actual,
    default: {
      ...actual,
      platform: 'linux',
      kill: vi.fn((pid: number) => {
        if (kernel.failure)
          throw kernel.failure
        if (!kernel.alive.has(pid))
          throw Object.assign(new Error('The controlled process does not exist.'), { code: 'ESRCH' })
        return true
      }),
    },
  }
})

vi.mock('node:child_process', async (importOriginal) => {
  const actual = await importOriginal<typeof import('node:child_process')>()
  return {
    ...actual,
    spawn: vi.fn(),

  }
})

vi.mock('./windowsCommandJob', () => ({ spawnWindowsCommandJob: vi.fn() }))

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date', 'setTimeout', 'clearTimeout', 'performance'] })
  vi.clearAllMocks()
  Object.defineProperty(process, 'platform', { value: 'linux', configurable: true })
  kernel.alive.clear()
  kernel.failure = undefined
})

afterEach(() => {
  vi.clearAllTimers()
  vi.useRealTimers()
})

function treeProcess(delay = 50) {
  const stub = createProcessStub()
  vi.mocked(spawn).mockReturnValue(stub.proc)
  kernel.alive.add(process.platform === 'win32' ? 123 : -123)
  const resource = spawnCommandProcess('controlled', ['argument'], { stdio: 'inherit', cwd: 'private-project' }, { ownTree: true, shutdownDelayMs: delay })
  return { ...stub, resource }
}

async function treeProcessAfterPipeEnd() {
  const current = treeProcess()
  const stdout = new PassThrough()
  Object.assign(current.emitter, { stdout })
  const ended = once(stdout, 'end')
  stdout.resume()
  stdout.end()
  await ended
  return current
}

function treeProcessWithProbeFailure(code = 'EPERM', afterSignal: NodeJS.Signals = 'SIGTERM') {
  const current = treeProcess()
  const stdout = new PassThrough()
  Object.assign(current.emitter, { stdout })
  const failure = Object.assign(new Error('The owned group probe failed before native exit.'), { code })
  const previousSignal = vi.mocked(process.kill).getMockImplementation()!
  let signalSent = false
  let failProbes = true
  vi.mocked(process.kill).mockImplementation((pid, signal) => {
    if (signal === 0 && signalSent && failProbes)
      throw failure
    const sent = previousSignal(pid, signal)
    if (signal === afterSignal)
      signalSent = true
    return sent
  })
  return {
    ...current,
    stdout,
    failure,
    allowProbes: () => { failProbes = false },
    restoreSignal: () => vi.mocked(process.kill).mockImplementation(previousSignal),
  }
}

describe('spawnCommandProcess', () => {
  it('preserves every ordinary spawn option and returns the exact child handle', async () => {
    const { emitter, proc } = createProcessStub({ exitCode: 0 })
    vi.mocked(spawn).mockReturnValue(proc)
    const options = { stdio: 'inherit' as const, env: { PRIVATE: 'yes' }, detached: false, cwd: 'private-project' }
    const resource = spawnCommandProcess('controlled', ['argument'], options)
    expect(vi.mocked(spawn)).toHaveBeenCalledExactlyOnceWith('controlled', ['argument'], options)
    expect(resource.child).toBe(proc)
    await resource.stop()
    expect(emitter.kill).not.toHaveBeenCalled()
    expect(process.kill).not.toHaveBeenCalled()
  })

  it('creates a private POSIX group without changing inherited stdin', async () => {
    const { resource, emitter } = treeProcess()
    expect(spawn).toHaveBeenCalledExactlyOnceWith('controlled', ['argument'], { stdio: 'inherit', cwd: 'private-project', detached: true })
    const stopped = resource.stop()
    await vi.advanceTimersByTimeAsync(0)
    expect(process.kill).toHaveBeenCalledWith(-123, 'SIGTERM')
    expect(emitter.kill).not.toHaveBeenCalled()
    emitter.exitCode = 0
    kernel.alive.delete(-123)
    emitter.emit('exit', 0, null)
    await stopped
    expect(vi.getTimerCount()).toBe(0)
  })

  it('keeps tree ownership when the caller changes its option object after spawn', async () => {
    const { emitter, proc } = createProcessStub()
    vi.mocked(spawn).mockReturnValue(proc)
    kernel.alive.add(-123)
    const ownership = { ownTree: true, shutdownDelayMs: 50 }
    const resource = spawnCommandProcess('controlled', [], {}, ownership)
    ownership.ownTree = false
    const stopped = resource.stop()
    await vi.advanceTimersByTimeAsync(0)
    expect(process.kill).toHaveBeenCalledWith(-123, 'SIGTERM')
    expect(emitter.kill).not.toHaveBeenCalled()
    kernel.alive.delete(-123)
    emitter.exitCode = 0
    emitter.emit('exit', 0, null)
    await stopped
  })

  it('keeps ordinary ownership when the caller requests a tree after spawn', async () => {
    const { emitter, proc } = createProcessStub()
    vi.mocked(spawn).mockReturnValue(proc)
    const ownership = { ownTree: false, shutdownDelayMs: 50 }
    const resource = spawnCommandProcess('controlled', [], {}, ownership)
    ownership.ownTree = true
    const stopped = resource.stop()
    expect(emitter.kill).toHaveBeenCalledWith('SIGTERM')
    expect(process.kill).not.toHaveBeenCalled()
    emitter.exitCode = 0
    emitter.emit('exit', 0, null)
    await stopped
  })

  it.each([0, -1, Number.NaN, Number.POSITIVE_INFINITY, 2_147_483_648])('rejects an invalid shutdown delay before spawning: %s', (delay) => {
    expect(() => spawnCommandProcess('controlled', [], {}, { ownTree: true, shutdownDelayMs: delay })).toThrow(RangeError)
    expect(spawn).not.toHaveBeenCalled()
    expect(process.kill).not.toHaveBeenCalled()
  })

  it('preserves a spawn failure without signaling any process', () => {
    const failure = new Error('The controlled spawn failed.')
    vi.mocked(spawn).mockImplementationOnce(() => {
      throw failure
    })
    expect(() => spawnCommandProcess('controlled', [], {}, { ownTree: true })).toThrow(failure)
    expect(process.kill).not.toHaveBeenCalled()
  })

  it('finishes an unsuccessful spawn without a PID', async () => {
    const { proc } = createProcessStub({ pid: undefined })
    vi.mocked(spawn).mockReturnValue(proc)
    await spawnCommandProcess('controlled', [], {}, { ownTree: true }).stop()
    expect(process.kill).not.toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(0)
  })

  it.each([0, 1, -1, Number.NaN, 2_147_483_648])('rejects an unsafe group PID without signaling: %s', async (pid) => {
    const { proc } = createProcessStub({ pid })
    vi.mocked(spawn).mockReturnValue(proc)
    await expect(spawnCommandProcess('controlled', [], {}, { ownTree: true }).stop()).rejects.toThrow(RangeError)
    expect(process.kill).not.toHaveBeenCalled()
  })

  it('rejects ownership of the current process', async () => {
    const { proc } = createProcessStub({ pid: process.pid })
    vi.mocked(spawn).mockReturnValue(proc)
    await expect(spawnCommandProcess('controlled', [], {}, { ownTree: true }).stop()).rejects.toThrow(RangeError)
    expect(process.kill).not.toHaveBeenCalled()
  })

  it('returns one shutdown promise for concurrent stop calls', async () => {
    const { emitter, resource } = treeProcess()
    const first = resource.stop()
    const second = resource.stop()
    expect(second).toBe(first)
    await vi.advanceTimersByTimeAsync(0)
    expect(vi.mocked(process.kill).mock.calls.filter(([, signal]) => signal === 'SIGTERM')).toHaveLength(1)
    emitter.exitCode = 0
    kernel.alive.delete(-123)
    emitter.emit('exit', 0, null)
    await first
  })

  it('waits for descendants after the root exits', async () => {
    const { emitter, resource } = treeProcess()
    let finished = false
    const stopped = resource.stop().then(() => {
      finished = true
    })
    await vi.advanceTimersByTimeAsync(0)
    emitter.exitCode = 0
    emitter.emit('exit', 0, null)
    await vi.advanceTimersByTimeAsync(25)
    expect(finished).toBe(false)
    kernel.alive.delete(-123)
    await vi.advanceTimersByTimeAsync(25)
    await stopped
    expect(finished).toBe(true)
    expect(emitter.listenerCount('exit')).toBe(0)
    expect(emitter.listenerCount('error')).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('stops descendants when the root exited before cleanup starts', async () => {
    const { emitter, resource } = treeProcess()
    emitter.exitCode = 0
    const stopped = resource.stop()
    await vi.advanceTimersByTimeAsync(0)
    expect(process.kill).toHaveBeenCalledWith(-123, 'SIGTERM')
    kernel.alive.delete(-123)
    await vi.advanceTimersByTimeAsync(25)
    await stopped
  })

  it('waits for the root exit event after the group disappears', async () => {
    const { emitter, resource } = treeProcess()
    kernel.alive.delete(-123)
    let finished = false
    const stopped = resource.stop().then(() => {
      finished = true
    })
    await vi.advanceTimersByTimeAsync(0)
    expect(finished).toBe(false)
    emitter.exitCode = 0
    emitter.emit('exit', 0, null)
    await stopped
    expect(finished).toBe(true)
  })

  it('escalates a surviving group after the graceful deadline', async () => {
    const { emitter, resource } = treeProcess()
    const stopped = resource.stop()
    await vi.advanceTimersByTimeAsync(49)
    expect(process.kill).not.toHaveBeenCalledWith(-123, 'SIGKILL')
    await vi.advanceTimersByTimeAsync(1)
    expect(process.kill).toHaveBeenCalledWith(-123, 'SIGKILL')
    kernel.alive.delete(-123)
    emitter.signalCode = 'SIGKILL'
    emitter.emit('exit', null, 'SIGKILL')
    await stopped
    expect(vi.getTimerCount()).toBe(0)
  })

  it.each([-60_000, 60_000])('uses elapsed time for shutdown when the wall clock changes by %s', async (change) => {
    const { emitter, resource } = treeProcess()
    const stopped = resource.stop()
    try {
      await vi.advanceTimersByTimeAsync(25)
      vi.setSystemTime(Date.now() + change)
      emitter.exitCode = 0
      emitter.emit('exit', 0, null)
      await vi.advanceTimersByTimeAsync(24)
      expect(process.kill).not.toHaveBeenCalledWith(-123, 'SIGKILL')
      await vi.advanceTimersByTimeAsync(1)
      expect(process.kill).toHaveBeenCalledWith(-123, 'SIGKILL')
    }
    finally {
      kernel.alive.delete(-123)
      emitter.signalCode = 'SIGKILL'
      emitter.emit('exit', null, 'SIGKILL')
      await vi.advanceTimersByTimeAsync(25)
      await stopped
    }
  })

  it('reports a group that survives forced termination and releases its listeners', async () => {
    const { emitter, resource } = treeProcess()
    const result = resource.stop().then(() => null, error => error)
    await vi.advanceTimersByTimeAsync(100)
    expect(await result).toMatchObject({ message: expect.stringContaining('process group 123') })
    expect(emitter.listenerCount('exit')).toBe(0)
    expect(emitter.listenerCount('error')).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('reports permission failures instead of treating the group as absent', async () => {
    const { emitter, resource } = treeProcess()
    const failure = Object.assign(new Error('The controlled signal failed.'), { code: 'EPERM' })
    kernel.failure = failure
    await expect(resource.stop()).rejects.toBe(failure)
    expect(emitter.listenerCount('exit')).toBe(0)
    expect(emitter.listenerCount('error')).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('waits for native exit and group disappearance after successful termination and probe EPERM without EOF', async () => {
    const { emitter, resource, stdout, allowProbes, restoreSignal } = treeProcessWithProbeFailure()
    let result: unknown
    const stopped = resource.stop().then(() => {
      result = 'finished'
    }, (error) => {
      result = error
    })
    try {
      await vi.advanceTimersByTimeAsync(0)
      expect(stdout.readableEnded).toBe(false)
      expect(vi.mocked(process.kill).mock.calls).toEqual([[-123, 'SIGTERM'], [-123, 0]])
      expect(result).toBeUndefined()
      allowProbes()
      kernel.alive.delete(-123)
      await vi.advanceTimersByTimeAsync(25)
      expect(result).toBeUndefined()
      emitter.signalCode = 'SIGTERM'
      emitter.emit('exit', null, 'SIGTERM')
      await stopped
      expect(result).toBe('finished')
      expect(vi.getTimerCount()).toBe(0)
      expect(emitter.listenerCount('exit')).toBe(0)
      expect(emitter.listenerCount('error')).toBe(0)
    }
    finally {
      allowProbes()
      kernel.alive.delete(-123)
      emitter.signalCode = 'SIGTERM'
      emitter.emit('exit', null, 'SIGTERM')
      await vi.advanceTimersByTimeAsync(100)
      await stopped
      restoreSignal()
      stdout.destroy()
    }
  })

  it('stops surviving descendants after successful termination and probe EPERM without EOF', async () => {
    const { emitter, resource, stdout, allowProbes, restoreSignal } = treeProcessWithProbeFailure()
    let result: unknown
    const stopped = resource.stop().then(() => {
      result = 'finished'
    }, (error) => {
      result = error
    })
    try {
      await vi.advanceTimersByTimeAsync(0)
      expect(stdout.readableEnded).toBe(false)
      expect(result).toBeUndefined()
      allowProbes()
      emitter.signalCode = 'SIGTERM'
      emitter.emit('exit', null, 'SIGTERM')
      await vi.advanceTimersByTimeAsync(0)
      expect(vi.mocked(process.kill).mock.calls.filter(([, signal]) => signal === 'SIGTERM')).toHaveLength(2)
      expect(result).toBeUndefined()
      kernel.alive.delete(-123)
      await vi.advanceTimersByTimeAsync(25)
      await stopped
      expect(result).toBe('finished')
      expect(vi.getTimerCount()).toBe(0)
      expect(emitter.listenerCount('exit')).toBe(0)
      expect(emitter.listenerCount('error')).toBe(0)
    }
    finally {
      allowProbes()
      kernel.alive.delete(-123)
      emitter.signalCode = 'SIGTERM'
      emitter.emit('exit', null, 'SIGTERM')
      await vi.advanceTimersByTimeAsync(100)
      await stopped
      restoreSignal()
      stdout.destroy()
    }
  })

  it('returns the original post-termination probe EPERM at the deadline when no native exit arrives', async () => {
    const { emitter, resource, stdout, failure, allowProbes, restoreSignal } = treeProcessWithProbeFailure()
    const unrelated = () => {}
    emitter.on('exit', unrelated)
    let result: unknown
    const stopped = resource.stop().then(() => {
      result = 'finished'
    }, (error) => {
      result = error
    })
    try {
      await vi.advanceTimersByTimeAsync(49)
      expect(stdout.readableEnded).toBe(false)
      expect(result).toBeUndefined()
      await vi.advanceTimersByTimeAsync(1)
      await stopped
      expect(result).toBe(failure)
      expect(process.kill).not.toHaveBeenCalledWith(-123, 'SIGKILL')
      expect(emitter.listeners('exit')).toEqual([unrelated])
      expect(emitter.listenerCount('error')).toBe(0)
      expect(vi.getTimerCount()).toBe(0)
    }
    finally {
      allowProbes()
      kernel.alive.delete(-123)
      emitter.signalCode = 'SIGTERM'
      emitter.emit('exit', null, 'SIGTERM')
      await vi.advanceTimersByTimeAsync(100)
      await stopped
      restoreSignal()
      stdout.destroy()
    }
  })

  it('preserves post-termination probe EACCES immediately without EOF', async () => {
    const { emitter, resource, stdout, failure, restoreSignal } = treeProcessWithProbeFailure('EACCES')
    try {
      await expect(resource.stop()).rejects.toBe(failure)
      expect(stdout.readableEnded).toBe(false)
      expect(vi.mocked(process.kill).mock.calls).toEqual([[-123, 'SIGTERM'], [-123, 0]])
      expect(emitter.listenerCount('exit')).toBe(0)
      expect(emitter.listenerCount('error')).toBe(0)
      expect(vi.getTimerCount()).toBe(0)
    }
    finally {
      restoreSignal()
      stdout.destroy()
    }
  })

  it('preserves post-exit probe EPERM after a successful termination signal', async () => {
    const { emitter, resource } = treeProcess()
    const failure = Object.assign(new Error('The owned group probe fails after native exit.'), { code: 'EPERM' })
    const previousSignal = vi.mocked(process.kill).getMockImplementation()!
    vi.mocked(process.kill).mockImplementation((pid, signal) => {
      if (signal === 0)
        throw failure
      const sent = previousSignal(pid, signal)
      emitter.signalCode = 'SIGTERM'
      emitter.emit('exit', null, 'SIGTERM')
      return sent
    })
    try {
      await expect(resource.stop()).rejects.toBe(failure)
      expect(vi.mocked(process.kill).mock.calls).toEqual([[-123, 'SIGTERM'], [-123, 0]])
      expect(emitter.listenerCount('exit')).toBe(0)
      expect(emitter.listenerCount('error')).toBe(0)
      expect(vi.getTimerCount()).toBe(0)
    }
    finally {
      vi.mocked(process.kill).mockImplementation(previousSignal)
    }
  })

  it('keeps forced termination and its deadline after successful SIGKILL and probe EPERM without EOF', async () => {
    const { emitter, resource, stdout, allowProbes, restoreSignal } = treeProcessWithProbeFailure('EPERM', 'SIGKILL')
    let result: unknown
    const stopped = resource.stop().then(() => {
      result = 'finished'
    }, (error) => {
      result = error
    })
    try {
      await vi.advanceTimersByTimeAsync(50)
      expect(stdout.readableEnded).toBe(false)
      expect(process.kill).toHaveBeenCalledWith(-123, 'SIGKILL')
      expect(result).toBeUndefined()
      await vi.advanceTimersByTimeAsync(25)
      allowProbes()
      emitter.signalCode = 'SIGKILL'
      emitter.emit('exit', null, 'SIGKILL')
      await vi.advanceTimersByTimeAsync(0)
      expect(vi.mocked(process.kill).mock.calls.filter(([, signal]) => signal === 'SIGTERM')).toHaveLength(1)
      expect(vi.mocked(process.kill).mock.calls.filter(([, signal]) => signal === 'SIGKILL')).toHaveLength(2)
      await vi.advanceTimersByTimeAsync(24)
      expect(result).toBeUndefined()
      await vi.advanceTimersByTimeAsync(1)
      await stopped
      expect(result).toMatchObject({ message: 'The owned process group 123 did not exit after forced termination.' })
      expect(vi.getTimerCount()).toBe(0)
      expect(emitter.listenerCount('exit')).toBe(0)
      expect(emitter.listenerCount('error')).toBe(0)
    }
    finally {
      allowProbes()
      kernel.alive.delete(-123)
      emitter.signalCode = 'SIGKILL'
      emitter.emit('exit', null, 'SIGKILL')
      await vi.advanceTimersByTimeAsync(100)
      await stopped
      restoreSignal()
      stdout.destroy()
    }
  })

  it('waits for native exit after a pipe ends and the owned group returns EPERM', async () => {
    const { emitter, resource } = await treeProcessAfterPipeEnd()
    const failure = Object.assign(new Error('The owned group awaits native exit.'), { code: 'EPERM' })
    kernel.failure = failure
    let result: unknown
    const stopped = resource.stop().then(() => {
      result = 'finished'
    }, (error) => {
      result = error
    })
    await vi.advanceTimersByTimeAsync(0)
    try {
      expect(result).toBeUndefined()
    }
    finally {
      kernel.failure = undefined
      kernel.alive.delete(-123)
      emitter.exitCode = 7
      emitter.emit('exit', 7, null)
      await stopped
    }
    expect(result).toBe('finished')
    expect(vi.getTimerCount()).toBe(0)
    expect(emitter.listenerCount('exit')).toBe(0)
    expect(emitter.listenerCount('error')).toBe(0)
  })

  it('stops surviving descendants after native exit resolves an EOF permission error', async () => {
    const { emitter, resource } = await treeProcessAfterPipeEnd()
    kernel.failure = Object.assign(new Error('The owned root awaits native exit.'), { code: 'EPERM' })
    let result: unknown
    const stopped = resource.stop().then(() => {
      result = 'finished'
    }, (error) => {
      result = error
    })
    await vi.advanceTimersByTimeAsync(0)
    try {
      expect(result).toBeUndefined()
      kernel.failure = undefined
      emitter.exitCode = 7
      emitter.emit('exit', 7, null)
      await vi.advanceTimersByTimeAsync(0)
      expect(vi.mocked(process.kill).mock.calls.filter(([, signal]) => signal === 'SIGTERM')).toHaveLength(2)
      expect(result).toBeUndefined()
    }
    finally {
      kernel.failure = undefined
      kernel.alive.delete(-123)
      emitter.exitCode = 7
      emitter.emit('exit', 7, null)
      await vi.advanceTimersByTimeAsync(25)
      await stopped
    }
    expect(result).toBe('finished')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('returns the original EOF permission error if no native exit arrives before the deadline', async () => {
    const { emitter, resource } = await treeProcessAfterPipeEnd()
    const failure = Object.assign(new Error('The owned process refuses its signal.'), { code: 'EPERM' })
    kernel.failure = failure
    let result: unknown
    const stopped = resource.stop().then(() => {
      result = 'finished'
    }, (error) => {
      result = error
    })
    await vi.advanceTimersByTimeAsync(0)
    try {
      expect(result).toBeUndefined()
    }
    finally {
      await vi.advanceTimersByTimeAsync(50)
      await stopped
    }
    expect(result).toBe(failure)
    expect(vi.getTimerCount()).toBe(0)
    expect(emitter.listenerCount('exit')).toBe(0)
    expect(emitter.listenerCount('error')).toBe(0)
  })

  it('keeps forced termination and its deadline after native exit resolves an EOF permission error', async () => {
    const { emitter, resource } = await treeProcessAfterPipeEnd()
    const failure = Object.assign(new Error('The owned root awaits its forced exit.'), { code: 'EPERM' })
    const previousSignal = vi.mocked(process.kill).getMockImplementation()!
    vi.mocked(process.kill).mockImplementation((pid, signal) => {
      if (signal === 'SIGKILL' && emitter.exitCode === null)
        throw failure
      if (!kernel.alive.has(pid))
        throw Object.assign(new Error('The controlled process does not exist.'), { code: 'ESRCH' })
      return true
    })
    let result: unknown
    const stopped = resource.stop().then(() => {
      result = 'finished'
    }, (error) => {
      result = error
    })
    try {
      await vi.advanceTimersByTimeAsync(50)
      expect(process.kill).toHaveBeenCalledWith(-123, 'SIGKILL')
      expect(result).toBeUndefined()
      await vi.advanceTimersByTimeAsync(25)
      emitter.exitCode = 7
      emitter.emit('exit', 7, null)
      await vi.advanceTimersByTimeAsync(0)
      expect(vi.mocked(process.kill).mock.calls.filter(([, signal]) => signal === 'SIGTERM')).toHaveLength(1)
      expect(vi.mocked(process.kill).mock.calls.filter(([, signal]) => signal === 'SIGKILL')).toHaveLength(2)
      await vi.advanceTimersByTimeAsync(24)
      expect(result).toBeUndefined()
      await vi.advanceTimersByTimeAsync(1)
      await stopped
      expect(result).toMatchObject({ message: 'The owned process group 123 did not exit after forced termination.' })
    }
    finally {
      kernel.alive.delete(-123)
      emitter.exitCode = 7
      emitter.emit('exit', 7, null)
      await vi.advanceTimersByTimeAsync(50)
      await stopped
      vi.mocked(process.kill).mockImplementation(previousSignal)
    }
    expect(vi.getTimerCount()).toBe(0)
    expect(emitter.listenerCount('exit')).toBe(0)
    expect(emitter.listenerCount('error')).toBe(0)
  })

  it.each(['EPERM', 'EACCES'])('preserves %s at EOF after native exit was already observed', async (code) => {
    const { emitter, resource } = await treeProcessAfterPipeEnd()
    emitter.exitCode = 7
    const failure = Object.assign(new Error('The owned descendant signal failed.'), { code })
    kernel.failure = failure
    await expect(resource.stop()).rejects.toBe(failure)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('preserves a non-EPERM signal failure at EOF before native exit', async () => {
    const { resource } = await treeProcessAfterPipeEnd()
    const failure = Object.assign(new Error('The owned signal failed.'), { code: 'EACCES' })
    kernel.failure = failure
    await expect(resource.stop()).rejects.toBe(failure)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('keeps cleanup finished when a queued EOF signal fails after a child error', async () => {
    const { emitter, resource } = await treeProcessAfterPipeEnd()
    kernel.failure = Object.assign(new Error('The owned group awaits native exit.'), { code: 'EPERM' })
    const failure = new Error('The controlled child failed before its signal.')
    const result = resource.stop().then(() => undefined, error => error)
    emitter.emit('error', failure)
    await vi.advanceTimersByTimeAsync(0)
    expect(await result).toBe(failure)
    expect(emitter.listenerCount('exit')).toBe(0)
    expect(emitter.listenerCount('error')).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('preserves a child error and removes only its shutdown listeners', async () => {
    const { emitter, resource } = treeProcess()
    const unrelated = () => {}
    emitter.on('exit', unrelated)
    const result = resource.stop().then(() => null, error => error)
    await vi.advanceTimersByTimeAsync(0)
    const failure = new Error('The controlled child failed.')
    emitter.emit('error', failure)
    expect(await result).toBe(failure)
    expect(emitter.listeners('exit')).toEqual([unrelated])
    expect(emitter.listenerCount('error')).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('delegates Windows tree ownership to its private job resource', async () => {
    Object.defineProperty(process, 'platform', { value: 'win32', configurable: true })
    const { proc } = createProcessStub()
    const stop = vi.fn().mockResolvedValue(undefined)
    const resource = { child: proc, stop }
    vi.mocked(spawnWindowsCommandJob).mockReturnValue(resource)
    const options = { stdio: 'inherit' as const, cwd: 'private-project' }
    expect(spawnCommandProcess('controlled', ['argument'], options, { ownTree: true, shutdownDelayMs: 50 })).toBe(resource)
    expect(spawnWindowsCommandJob).toHaveBeenCalledExactlyOnceWith('controlled', ['argument'], options, 50)
    await resource.stop()
    expect(stop).toHaveBeenCalledOnce()
    expect(spawn).not.toHaveBeenCalled()
    expect(process.kill).not.toHaveBeenCalled()
  })

  it('keeps ordinary Windows commands on their exact spawn and process handles', async () => {
    Object.defineProperty(process, 'platform', { value: 'win32', configurable: true })
    const { proc } = createProcessStub({ exitCode: 0 })
    vi.mocked(spawn).mockReturnValue(proc)
    const options = { stdio: 'inherit' as const, detached: true }
    const resource = spawnCommandProcess('controlled', [], options)
    expect(resource.child).toBe(proc)
    expect(spawn).toHaveBeenCalledExactlyOnceWith('controlled', [], options)
    expect(spawnWindowsCommandJob).not.toHaveBeenCalled()
    await resource.stop()
    expect(process.kill).not.toHaveBeenCalled()
  })
})
