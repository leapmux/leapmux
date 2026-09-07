import type { LeadingThrottled } from './throttle'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { leadingThrottle } from './throttle'

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

describe('leadingThrottle', () => {
  it('runs the first call immediately', () => {
    const fn = vi.fn()
    const throttled = leadingThrottle(fn, 300)
    throttled()

    expect(fn).toHaveBeenCalledTimes(1)
  })

  it('costs exactly one call when nothing follows', () => {
    const fn = vi.fn()
    const throttled = leadingThrottle(fn, 300)
    throttled()
    vi.advanceTimersByTime(900)

    expect(fn).toHaveBeenCalledTimes(1)
  })

  it('collapses a burst into the first call and one trailing call', () => {
    const fn = vi.fn()
    const throttled = leadingThrottle(fn, 300)
    for (let i = 0; i < 6; i++)
      throttled()

    expect(fn).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(300)
    expect(fn).toHaveBeenCalledTimes(2)
  })

  it('runs the trailing call after the burst, so it sees the last write', () => {
    const fn = vi.fn()
    const throttled = leadingThrottle(fn, 300)
    throttled()
    vi.advanceTimersByTime(299)
    throttled()

    expect(fn).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(1)
    expect(fn).toHaveBeenCalledTimes(2)
  })

  it('opens a new window after a trailing call', () => {
    const fn = vi.fn()
    const throttled = leadingThrottle(fn, 300)
    throttled()
    throttled()
    vi.advanceTimersByTime(300) // the trailing call runs
    throttled()
    expect(fn).toHaveBeenCalledTimes(2)

    vi.advanceTimersByTime(300)
    expect(fn).toHaveBeenCalledTimes(3)
  })

  it('runs immediately again once a quiet window passes', () => {
    const fn = vi.fn()
    const throttled = leadingThrottle(fn, 300)
    throttled()
    vi.advanceTimersByTime(300)
    throttled()

    expect(fn).toHaveBeenCalledTimes(2)
  })

  it('takes the window from the caller', () => {
    const fn = vi.fn()
    const throttled = leadingThrottle(fn, 1000)
    throttled()
    throttled()
    vi.advanceTimersByTime(999)
    expect(fn).toHaveBeenCalledTimes(1)

    vi.advanceTimersByTime(1)
    expect(fn).toHaveBeenCalledTimes(2)
  })

  it('drops a pending trailing call on cancel', () => {
    const fn = vi.fn()
    const throttled = leadingThrottle(fn, 300)
    throttled()
    throttled()
    throttled.cancel()
    vi.advanceTimersByTime(900)

    expect(fn).toHaveBeenCalledTimes(1)
  })

  it('cancels a quiet window too, so the next call leads again', () => {
    // Without the clearTimeout the window would still be open, and the next
    // call would wait for a trailing edge instead of running at once.
    const fn = vi.fn()
    const throttled = leadingThrottle(fn, 300)
    throttled()
    throttled.cancel()
    throttled()

    expect(fn).toHaveBeenCalledTimes(2)
  })

  it('coalesces a call fn makes back into the throttle', () => {
    // The window is open for the whole of fn, so a reentrant call cannot run a
    // second invocation inside it.
    let throttled: LeadingThrottled
    let reentered = false
    const fn = vi.fn(() => {
      if (reentered)
        return
      reentered = true
      throttled()
    })
    throttled = leadingThrottle(fn, 300)
    throttled()

    expect(fn).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(300)
    expect(fn).toHaveBeenCalledTimes(2)
  })

  it('closes the window when fn cancels from inside itself', () => {
    // A caller whose invocation did nothing says so with cancel(), and the next
    // call leads again rather than waiting out a window that spent nothing.
    let throttled: LeadingThrottled
    let cancelNext = false
    const fn = vi.fn(() => {
      if (cancelNext)
        throttled.cancel()
    })
    throttled = leadingThrottle(fn, 300)
    throttled()
    expect(fn).toHaveBeenCalledTimes(1)

    cancelNext = true
    vi.advanceTimersByTime(300) // the trailing call runs and cancels
    throttled()

    expect(fn).toHaveBeenCalledTimes(2)
  })

  it('tolerates a cancel with nothing in flight', () => {
    const fn = vi.fn()
    const throttled = leadingThrottle(fn, 300)
    expect(() => {
      throttled.cancel()
      throttled.cancel()
    }).not.toThrow()
    expect(fn).not.toHaveBeenCalled()
  })
})
