import { afterEach, describe, expect, it, vi } from 'vitest'
import { cancelFrame, requestFrame, scheduleDeferredFrame } from './animationFrame'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('requestFrame', () => {
  it('runs the callback in the animation frame, with no timestamp, and returns its handle', () => {
    const frames: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', vi.fn((frame: FrameRequestCallback) => {
      frames.push(frame)
      return 41
    }))
    const cb = vi.fn()

    expect(requestFrame(cb)).toBe(41)
    expect(cb).not.toHaveBeenCalled()

    frames[0]!(16)
    expect(cb).toHaveBeenCalledOnce()
    expect(cb).toHaveBeenCalledWith()
  })

  it('runs the callback at once and returns 0 without requestAnimationFrame', () => {
    vi.stubGlobal('requestAnimationFrame', undefined)
    const cb = vi.fn()

    expect(requestFrame(cb)).toBe(0)
    expect(cb).toHaveBeenCalledOnce()
  })
})

describe('cancelFrame', () => {
  it('cancels the animation frame of the handle', () => {
    const cancel = vi.fn()
    vi.stubGlobal('cancelAnimationFrame', cancel)

    cancelFrame(7)

    expect(cancel).toHaveBeenCalledExactlyOnceWith(7)
  })

  it('does nothing without cancelAnimationFrame', () => {
    vi.stubGlobal('cancelAnimationFrame', undefined)

    expect(() => cancelFrame(7)).not.toThrow()
  })
})

describe('scheduleDeferredFrame', () => {
  it('cancels through the frame API that scheduled the work', () => {
    const request = vi.fn(() => 71)
    const cancel = vi.fn()
    const otherCancel = vi.fn()
    vi.stubGlobal('requestAnimationFrame', request)
    vi.stubGlobal('cancelAnimationFrame', cancel)
    const callback = vi.fn()

    const stop = scheduleDeferredFrame(callback)
    vi.stubGlobal('cancelAnimationFrame', otherCancel)
    stop()

    expect(request).toHaveBeenCalledOnce()
    expect(callback).not.toHaveBeenCalled()
    expect(cancel).toHaveBeenCalledExactlyOnceWith(71)
    expect(otherCancel).not.toHaveBeenCalled()
  })

  it('defers to a cancellable timer if the frame APIs are incomplete', () => {
    vi.useFakeTimers()
    vi.stubGlobal('requestAnimationFrame', vi.fn())
    vi.stubGlobal('cancelAnimationFrame', undefined)
    const delivered = vi.fn()
    const discarded = vi.fn()
    scheduleDeferredFrame(delivered)
    const stop = scheduleDeferredFrame(discarded)

    expect(delivered).not.toHaveBeenCalled()
    stop()
    vi.runAllTimers()

    expect(delivered).toHaveBeenCalledExactlyOnceWith()
    expect(discarded).not.toHaveBeenCalled()
  })
})
