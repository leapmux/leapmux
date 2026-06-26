import { afterEach, describe, expect, it, vi } from 'vitest'
import { cancelIdle, requestIdle } from './idleCallback'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('idlecallback', () => {
  it('uses requestIdleCallback when available', () => {
    const ric = vi.fn(() => 42)
    const cic = vi.fn()
    vi.stubGlobal('requestIdleCallback', ric)
    vi.stubGlobal('cancelIdleCallback', cic)

    const cb = () => {}
    const handle = requestIdle(cb)
    expect(ric).toHaveBeenCalledWith(cb)
    expect(handle).toBe(42)

    cancelIdle(handle)
    expect(cic).toHaveBeenCalledWith(42)
  })

  it('falls back to timers under a PARTIAL polyfill (request present, cancel absent)', () => {
    // The two must be used as a pair: if requestIdleCallback exists but
    // cancelIdleCallback does not, scheduling via requestIdleCallback would leave the
    // handle un-cancellable (clearTimeout on an idle id silently no-ops). The paired
    // check routes BOTH through the timer fallback so cancel actually cancels.
    const ric = vi.fn(() => 99)
    vi.stubGlobal('requestIdleCallback', ric)
    vi.stubGlobal('cancelIdleCallback', undefined)
    vi.useFakeTimers()

    const cb = vi.fn()
    cancelIdle(requestIdle(cb))
    expect(ric).not.toHaveBeenCalled() // did NOT use the unpaired requestIdleCallback
    vi.advanceTimersByTime(2)
    expect(cb).not.toHaveBeenCalled() // the timer handle was genuinely cancelled
  })

  it('falls back to setTimeout/clearTimeout when requestIdleCallback is absent', () => {
    vi.stubGlobal('requestIdleCallback', undefined)
    vi.stubGlobal('cancelIdleCallback', undefined)
    vi.useFakeTimers()

    const cb = vi.fn()
    requestIdle(cb)
    expect(cb).not.toHaveBeenCalled()

    // The scheduled fallback fires after the timer advances.
    vi.advanceTimersByTime(2)
    expect(cb).toHaveBeenCalledTimes(1)

    // Cancelling a fresh handle clears the timer (no throw, no fire).
    const cb2 = vi.fn()
    cancelIdle(requestIdle(cb2))
    vi.advanceTimersByTime(2)
    expect(cb2).not.toHaveBeenCalled()
  })
})
