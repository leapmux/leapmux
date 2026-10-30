import { Clock, createTimer } from '@milkdown/ctx'
import { afterEach, describe, expect, it, vi } from 'vitest'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('createTimer', () => {
  it('releases its timeout when readiness resolves before the document is removed', async () => {
    vi.useFakeTimers()
    const clock = new Clock()
    const timer = createTimer('ready-before-document-removal', 3000).create(clock.store)
    const completion = timer.start()
    expect(timer.start()).toBe(completion)
    expect(vi.getTimerCount()).toBe(1)
    timer.done()
    await completion
    expect(timer.status).toBe('resolved')
    expect(vi.getTimerCount()).toBe(0)
    vi.stubGlobal('removeEventListener', undefined)
    expect(() => vi.advanceTimersByTime(3000)).not.toThrow()
  })

  it('still rejects an unresolved readiness timer and removes its timeout', async () => {
    vi.useFakeTimers()
    const clock = new Clock()
    const timer = createTimer('unresolved-readiness', 17).create(clock.store)
    const result = timer.start().catch((error: unknown) => error)
    vi.advanceTimersByTime(17)
    expect(await result).toEqual(new Error('Timing unresolved-readiness timeout.'))
    expect(timer.status).toBe('rejected')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('keeps timers with the same type in separate clocks independent', async () => {
    vi.useFakeTimers()
    const type = createTimer('independent-readiness', 17)
    const first = type.create(new Clock().store)
    const second = type.create(new Clock().store)
    const firstCompletion = first.start()
    const secondCompletion = second.start()
    const secondFailure = secondCompletion.catch((error: unknown) => error)
    expect(vi.getTimerCount()).toBe(2)
    first.done()
    await firstCompletion
    expect(first.status).toBe('resolved')
    expect(second.status).toBe('pending')
    expect(vi.getTimerCount()).toBe(1)
    vi.advanceTimersByTime(17)
    expect(await secondFailure).toEqual(new Error('Timing independent-readiness timeout.'))
    second.done()
    expect(second.status).toBe('rejected')
    expect(second.start()).toBe(secondCompletion)
    expect(first.status).toBe('resolved')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('does not schedule more timeouts after repeated readiness completion', async () => {
    vi.useFakeTimers()
    const timer = createTimer('repeated-readiness', 17).create(new Clock().store)
    const completion = timer.start()
    timer.done()
    await completion
    timer.done()
    expect(timer.start()).toBe(completion)
    timer.done()
    expect(timer.status).toBe('resolved')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('releases a timeout whose native handle is zero', async () => {
    const clear = vi.fn()
    vi.stubGlobal('setTimeout', () => 0)
    vi.stubGlobal('clearTimeout', clear)
    const timer = createTimer('zero-timeout-handle').create(new Clock().store)
    const completion = timer.start()
    timer.done()
    await completion
    expect(clear).toHaveBeenCalledExactlyOnceWith(0)
  })
})
