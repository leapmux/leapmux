import { afterEach, describe, expect, it, vi } from 'vitest'
import { cancelIdle, FALLBACK_IDLE_BUDGET_MS, requestIdle } from './idleCallback'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('idleCallback', () => {
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

  // `initStorageCleanup` caps how long its sweep waits for an idle window, so a
  // page that never goes idle still sweeps. The fallback ignores the option,
  // because a timer already runs the callback immediately.
  it('forwards a timeout to requestIdleCallback', () => {
    const ric = vi.fn(() => 42)
    vi.stubGlobal('requestIdleCallback', ric)
    vi.stubGlobal('cancelIdleCallback', vi.fn())

    const cb = () => {}
    requestIdle(cb, { timeout: 5000 })

    expect(ric).toHaveBeenCalledWith(cb, { timeout: 5000 })
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

  it('gives the fallback callback a deadline of one work slice', () => {
    vi.stubGlobal('requestIdleCallback', undefined)
    vi.stubGlobal('cancelIdleCallback', undefined)
    vi.useFakeTimers()

    let deadline: IdleDeadline | undefined
    requestIdle((received) => {
      deadline = received
    })
    vi.advanceTimersByTime(2)

    expect(deadline?.didTimeout).toBe(false)
    const remaining = deadline?.timeRemaining() ?? -1
    expect(remaining).toBeGreaterThan(0)
    expect(remaining).toBeLessThanOrEqual(FALLBACK_IDLE_BUDGET_MS)
    vi.advanceTimersByTime(FALLBACK_IDLE_BUDGET_MS)
    expect(deadline?.timeRemaining()).toBe(0)
  })
})
