import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRafCoalescer } from '~/lib/rafCoalesce'

describe('createRafCoalescer', () => {
  let rafCallbacks: FrameRequestCallback[] = []
  let nextRafId = 0
  let originalRaf: typeof globalThis.requestAnimationFrame
  let originalCaf: typeof globalThis.cancelAnimationFrame
  const flushFrame = () => {
    const cbs = rafCallbacks
    rafCallbacks = []
    for (const cb of cbs)
      cb(performance.now())
  }
  beforeEach(() => {
    rafCallbacks = []
    nextRafId = 0
    originalRaf = globalThis.requestAnimationFrame
    originalCaf = globalThis.cancelAnimationFrame
    globalThis.requestAnimationFrame = ((cb: FrameRequestCallback) => {
      const id = ++nextRafId
      rafCallbacks.push(cb)
      return id
    }) as typeof globalThis.requestAnimationFrame
    globalThis.cancelAnimationFrame = (() => {
      // Tests dispatch frames manually; cancellation just clears the
      // queue without invoking pending callbacks.
      rafCallbacks = []
    }) as typeof globalThis.cancelAnimationFrame
  })
  afterEach(() => {
    globalThis.requestAnimationFrame = originalRaf
    globalThis.cancelAnimationFrame = originalCaf
  })

  it('coalesces multiple pushes into one handler dispatch with the latest event', () => {
    const handler = vi.fn()
    const c = createRafCoalescer<number>(handler)
    c.push(1)
    c.push(2)
    c.push(3)
    expect(handler).not.toHaveBeenCalled()
    flushFrame()
    expect(handler).toHaveBeenCalledTimes(1)
    expect(handler).toHaveBeenCalledWith(3)
  })

  it('flush() dispatches synchronously and cancels the pending frame', () => {
    const handler = vi.fn()
    const c = createRafCoalescer<number>(handler)
    c.push(7)
    c.flush()
    expect(handler).toHaveBeenCalledTimes(1)
    expect(handler).toHaveBeenCalledWith(7)
    // Frame queue was cleared; running it again is a no-op.
    flushFrame()
    expect(handler).toHaveBeenCalledTimes(1)
  })

  it('flush() is a no-op when nothing is pending', () => {
    const handler = vi.fn()
    const c = createRafCoalescer<number>(handler)
    c.flush()
    expect(handler).not.toHaveBeenCalled()
  })

  it('abort() drops the pending event without dispatching', () => {
    const handler = vi.fn()
    const c = createRafCoalescer<number>(handler)
    c.push(1)
    c.abort()
    flushFrame()
    expect(handler).not.toHaveBeenCalled()
  })

  it('push after flush schedules a fresh frame', () => {
    const handler = vi.fn()
    const c = createRafCoalescer<number>(handler)
    c.push(1)
    c.flush()
    c.push(2)
    expect(handler).toHaveBeenCalledTimes(1)
    flushFrame()
    expect(handler).toHaveBeenCalledTimes(2)
    expect(handler.mock.calls[1][0]).toBe(2)
  })
})
