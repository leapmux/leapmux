import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createProcessStub } from '~/test-support/childProcess'
import { stopProcess, stopProcesses } from './process'

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

describe('process shutdown', () => {
  it.each([0, -1, Number.NaN, Number.POSITIVE_INFINITY, 2_147_483_648])('rejects an invalid shutdown delay without signaling the process: %s', async (delay) => {
    const { emitter, proc } = createProcessStub()
    await expect(stopProcess(proc, delay)).rejects.toBeInstanceOf(RangeError)
    expect(emitter.kill).not.toHaveBeenCalled()
    expect(emitter.listenerCount('exit')).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
  })

  it.each([{ pid: undefined }, { signalCode: 'SIGTERM' as const }])('skips a process without a live handle: %j', async (options) => {
    const { emitter, proc } = createProcessStub(options)
    await stopProcess(proc)
    expect(emitter.kill).not.toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('releases listeners and timers after an asynchronous process error', async () => {
    const { emitter, proc } = createProcessStub()
    const result = stopProcess(proc).then(() => null, error => error)
    const error = new Error('Asynchronous signal failure')
    emitter.emit('error', error)
    expect(await result).toBe(error)
    expect(emitter.listenerCount('error')).toBe(0)
    expect(emitter.listenerCount('exit')).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('returns immediately for an exited process without sending another signal', async () => {
    const { emitter, proc } = createProcessStub()
    emitter.exitCode = 0
    await stopProcess(proc)
    expect(emitter.kill).not.toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('finishes as soon as the process exits and clears its timers', async () => {
    const { emitter, proc } = createProcessStub()
    const stopped = stopProcess(proc)
    expect(emitter.kill).toHaveBeenCalledExactlyOnceWith('SIGTERM')
    emitter.exitCode = 0
    emitter.emit('exit', 0, null)
    await stopped
    expect(vi.getTimerCount()).toBe(0)
    expect(emitter.listenerCount('exit')).toBe(0)
  })

  it('escalates only when the graceful shutdown deadline passes', async () => {
    const { emitter, proc } = createProcessStub()
    const stopped = stopProcess(proc, 50)
    await vi.advanceTimersByTimeAsync(49)
    expect(emitter.kill).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(emitter.kill).toHaveBeenLastCalledWith('SIGKILL')
    emitter.signalCode = 'SIGKILL'
    emitter.emit('exit', null, 'SIGKILL')
    await stopped
    expect(vi.getTimerCount()).toBe(0)
  })

  it('reports a process that survives SIGKILL and releases its listeners', async () => {
    const { emitter, proc } = createProcessStub()
    const stopped = stopProcess(proc, 50)
    const result = stopped.then(() => null, error => error)
    await vi.advanceTimersByTimeAsync(100)
    expect(await result).toBeInstanceOf(Error)
    expect(String(await result)).toContain('123')
    expect(emitter.listenerCount('exit')).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('starts each shutdown before it waits for any process', async () => {
    const first = createProcessStub()
    const second = createProcessStub()
    const stopped = stopProcesses([first.proc, second.proc])
    expect(first.emitter.kill).toHaveBeenCalledWith('SIGTERM')
    expect(second.emitter.kill).toHaveBeenCalledWith('SIGTERM')
    first.emitter.emit('exit', 0, null)
    second.emitter.emit('exit', 0, null)
    await stopped
    expect(vi.getTimerCount()).toBe(0)
  })

  it('reports a signal failure without retaining timers or listeners', async () => {
    const { emitter, proc } = createProcessStub()
    const error = new Error('signal refused')
    emitter.kill.mockImplementation(() => {
      throw error
    })
    const result = stopProcess(proc, 50).then(() => null, reason => reason)
    await vi.advanceTimersByTimeAsync(100)
    expect(await result).toBe(error)
    expect(emitter.listenerCount('exit')).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('waits for the other processes before reporting a failed shutdown', async () => {
    const first = createProcessStub()
    const second = createProcessStub()
    first.emitter.kill.mockImplementation(() => {
      throw new Error('signal refused')
    })
    let finished = false
    const result = stopProcesses([first.proc, second.proc], 50)
      .then(() => null, error => error)
      .finally(() => { finished = true })
    await vi.advanceTimersByTimeAsync(1)
    expect(finished).toBe(false)
    second.emitter.emit('exit', 0, null)
    await vi.advanceTimersByTimeAsync(100)
    expect(await result).toBeInstanceOf(AggregateError)
    expect(vi.getTimerCount()).toBe(0)
  })
})
