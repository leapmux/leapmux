import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createExponentialBackoff } from '~/lib/retry'

beforeEach(() => {
  vi.useFakeTimers()
  // Pin Math.random to 0.5 so the symmetric-jitter offset is zero and
  // delay assertions match the base sequence exactly. Tests that
  // exercise jitter explicitly override this.
  vi.spyOn(Math, 'random').mockReturnValue(0.5)
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('createExponentialBackoff', () => {
  it('schedules the first retry at exactly `initialMs` (jitter pinned to 0)', () => {
    const backoff = createExponentialBackoff<string>({ initialMs: 500, maxMs: 10_000 })
    const fire = vi.fn()
    const delay = backoff.schedule('k', fire)
    expect(delay).toBe(500)

    vi.advanceTimersByTime(499)
    expect(fire).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(fire).toHaveBeenCalledTimes(1)
  })

  it('doubles the delay on each schedule until it hits `maxMs`', () => {
    const backoff = createExponentialBackoff<string>({ initialMs: 500, maxMs: 4000 })
    // peekNextDelay returns the un-jittered base; with random pinned at
    // 0.5 the arm-time matches it exactly.
    expect(backoff.peekNextDelay('k')).toBe(500)
    expect(backoff.schedule('k', () => {})).toBe(500)
    vi.runAllTimers()
    expect(backoff.schedule('k', () => {})).toBe(1000)
    vi.runAllTimers()
    expect(backoff.schedule('k', () => {})).toBe(2000)
    vi.runAllTimers()
    expect(backoff.schedule('k', () => {})).toBe(4000)
    vi.runAllTimers()
    // Already at max: subsequent schedules stay at maxMs.
    expect(backoff.schedule('k', () => {})).toBe(4000)
    vi.runAllTimers()
    expect(backoff.schedule('k', () => {})).toBe(4000)
  })

  it('honors a non-default multiplier', () => {
    const backoff = createExponentialBackoff<string>({ initialMs: 100, maxMs: 1000, multiplier: 3, jitterFactor: 0 })
    expect(backoff.schedule('k', () => {})).toBe(100)
    vi.runAllTimers()
    expect(backoff.schedule('k', () => {})).toBe(300)
    vi.runAllTimers()
    expect(backoff.schedule('k', () => {})).toBe(900)
    vi.runAllTimers()
    expect(backoff.schedule('k', () => {})).toBe(1000) // clamped
  })

  it('treats schedule as a no-op when a timer is already pending for that key', () => {
    const backoff = createExponentialBackoff<string>({ initialMs: 500, maxMs: 10_000 })
    const fire1 = vi.fn()
    const fire2 = vi.fn()
    expect(backoff.schedule('k', fire1)).toBe(500)
    // Second call returns null (already pending); the original `fire`
    // is the one that will run — the new `fire2` is dropped.
    expect(backoff.schedule('k', fire2)).toBeNull()
    expect(backoff.peekNextDelay('k')).toBeNull()
    vi.runAllTimers()
    expect(fire1).toHaveBeenCalledTimes(1)
    expect(fire2).not.toHaveBeenCalled()
  })

  it('keeps per-key delays independent', () => {
    const backoff = createExponentialBackoff<string>({ initialMs: 500, maxMs: 4000 })
    backoff.schedule('a', () => {})
    backoff.schedule('b', () => {})
    vi.runAllTimers()
    // Both keys advanced one step.
    expect(backoff.peekNextDelay('a')).toBe(1000)
    expect(backoff.peekNextDelay('b')).toBe(1000)
    // Advance only `a` again.
    backoff.schedule('a', () => {})
    vi.runAllTimers()
    expect(backoff.peekNextDelay('a')).toBe(2000)
    expect(backoff.peekNextDelay('b')).toBe(1000)
  })

  it('reset(key) cancels a pending timer and restarts the sequence', () => {
    const backoff = createExponentialBackoff<string>({ initialMs: 500, maxMs: 10_000 })
    const fire = vi.fn()
    backoff.schedule('k', fire)
    vi.runAllTimers() // first retry fires
    expect(fire).toHaveBeenCalledTimes(1)
    // Now the next delay would be 1000ms. Reset, and the next schedule
    // restarts at 500ms.
    backoff.reset('k')
    expect(backoff.peekNextDelay('k')).toBe(500)
    expect(backoff.schedule('k', fire)).toBe(500)
  })

  it('reset(key) clears a still-pending timer so `fire` never runs', () => {
    const backoff = createExponentialBackoff<string>({ initialMs: 500, maxMs: 10_000 })
    const fire = vi.fn()
    backoff.schedule('k', fire)
    backoff.reset('k')
    vi.runAllTimers()
    expect(fire).not.toHaveBeenCalled()
    expect(backoff.size()).toBe(0)
  })

  it('reset(key) on an unknown key is a no-op', () => {
    const backoff = createExponentialBackoff<string>({ initialMs: 500, maxMs: 10_000 })
    expect(() => backoff.reset('never-scheduled')).not.toThrow()
    expect(backoff.size()).toBe(0)
  })

  it('cancelAll() cancels every pending timer and forgets every delay', () => {
    const backoff = createExponentialBackoff<string>({ initialMs: 500, maxMs: 10_000 })
    const fireA = vi.fn()
    const fireB = vi.fn()
    backoff.schedule('a', fireA)
    backoff.schedule('b', fireB)
    expect(backoff.size()).toBe(2)
    backoff.cancelAll()
    expect(backoff.size()).toBe(0)
    vi.runAllTimers()
    expect(fireA).not.toHaveBeenCalled()
    expect(fireB).not.toHaveBeenCalled()
    // Delays were forgotten too — next schedule starts fresh.
    expect(backoff.peekNextDelay('a')).toBe(500)
  })

  it('allows `fire` to re-schedule the same key (slot is freed before the callback runs)', () => {
    const backoff = createExponentialBackoff<string>({ initialMs: 500, maxMs: 10_000 })
    let runs = 0
    const fire = () => {
      runs++
      if (runs < 3)
        backoff.schedule('k', fire)
    }
    backoff.schedule('k', fire)

    vi.advanceTimersByTime(500)
    expect(runs).toBe(1)
    vi.advanceTimersByTime(1000)
    expect(runs).toBe(2)
    vi.advanceTimersByTime(2000)
    expect(runs).toBe(3)
    // After the third run, `fire` doesn't re-schedule.
    expect(backoff.size()).toBe(0)
  })

  it('peekNextDelay returns null while a timer is pending', () => {
    const backoff = createExponentialBackoff<string>({ initialMs: 500, maxMs: 10_000 })
    expect(backoff.peekNextDelay('k')).toBe(500)
    backoff.schedule('k', () => {})
    expect(backoff.peekNextDelay('k')).toBeNull()
  })

  it('supports keys that are objects (uses Map identity)', () => {
    const backoff = createExponentialBackoff<{ id: string }>({ initialMs: 500, maxMs: 10_000 })
    const a = { id: 'a' }
    const b = { id: 'a' } // same shape, different identity
    backoff.schedule(a, () => {})
    backoff.schedule(b, () => {})
    expect(backoff.size()).toBe(2)
  })

  it('rejects invalid options at construction time', () => {
    expect(() => createExponentialBackoff({ initialMs: 0, maxMs: 1000 })).toThrow(/initialMs/)
    expect(() => createExponentialBackoff({ initialMs: -1, maxMs: 1000 })).toThrow(/initialMs/)
    expect(() => createExponentialBackoff({ initialMs: 500, maxMs: 100 })).toThrow(/maxMs/)
    expect(() => createExponentialBackoff({ initialMs: 500, maxMs: 1000, multiplier: 1 })).toThrow(/multiplier/)
    expect(() => createExponentialBackoff({ initialMs: 500, maxMs: 1000, multiplier: 0 })).toThrow(/multiplier/)
    expect(() => createExponentialBackoff({ initialMs: 500, maxMs: 1000, jitterFactor: -0.1 })).toThrow(/jitterFactor/)
    expect(() => createExponentialBackoff({ initialMs: 500, maxMs: 1000, jitterFactor: 1 })).toThrow(/jitterFactor/)
  })

  describe('jitter', () => {
    // The four interesting points in the [0,1) random space are 0
    // (lower bound), 0.5 (mid / no offset), just under 1 (upper bound),
    // and a representative intermediate.
    it('applies ±jitterFactor symmetric spread around the base delay', () => {
      // Math.random() = 0 → minimum: base * (1 - jitterFactor).
      vi.spyOn(Math, 'random').mockReturnValue(0)
      let backoff = createExponentialBackoff<string>({ initialMs: 1000, maxMs: 10_000, jitterFactor: 0.2 })
      expect(backoff.schedule('a', () => {})).toBe(800)

      // Math.random() ≈ 1 → upper bound (open interval): base * (1 + jitterFactor - ε).
      vi.spyOn(Math, 'random').mockReturnValue(0.99999)
      backoff = createExponentialBackoff<string>({ initialMs: 1000, maxMs: 10_000, jitterFactor: 0.2 })
      const upper = backoff.schedule('a', () => {})!
      expect(upper).toBeGreaterThan(1199)
      expect(upper).toBeLessThan(1201)

      // Math.random() = 0.5 → exact base (offset zero).
      vi.spyOn(Math, 'random').mockReturnValue(0.5)
      backoff = createExponentialBackoff<string>({ initialMs: 1000, maxMs: 10_000, jitterFactor: 0.2 })
      expect(backoff.schedule('a', () => {})).toBe(1000)
    })

    it('defaults jitterFactor to 0.2 (±20%) — matches backend convention', () => {
      // Lower bound at 80% of base.
      vi.spyOn(Math, 'random').mockReturnValue(0)
      const backoff = createExponentialBackoff<string>({ initialMs: 1000, maxMs: 10_000 })
      expect(backoff.schedule('a', () => {})).toBe(800)
    })

    it('doubling is driven by the un-jittered base, not the jittered delay', () => {
      // Pin random low so jitter SHRINKS each delay. If doubling were
      // driven by the jittered value, the sequence would compress to
      // 800 → 1600 → 3200 → 6400 instead of 1000 → 2000 → 4000 → 8000.
      vi.spyOn(Math, 'random').mockReturnValue(0)
      const backoff = createExponentialBackoff<string>({ initialMs: 1000, maxMs: 10_000, jitterFactor: 0.2 })
      // peekNextDelay reports the un-jittered base.
      expect(backoff.peekNextDelay('k')).toBe(1000)
      backoff.schedule('k', () => {})
      vi.runAllTimers()
      expect(backoff.peekNextDelay('k')).toBe(2000)
      backoff.schedule('k', () => {})
      vi.runAllTimers()
      expect(backoff.peekNextDelay('k')).toBe(4000)
      backoff.schedule('k', () => {})
      vi.runAllTimers()
      expect(backoff.peekNextDelay('k')).toBe(8000)
    })

    it('jitterFactor: 0 makes scheduled delay exactly match peekNextDelay', () => {
      // Force a non-mid random so a non-zero jitterFactor would shift
      // the delay. With jitterFactor: 0 the result must still equal
      // the base.
      vi.spyOn(Math, 'random').mockReturnValue(0)
      const backoff = createExponentialBackoff<string>({ initialMs: 1000, maxMs: 10_000, jitterFactor: 0 })
      expect(backoff.schedule('k', () => {})).toBe(1000)
    })

    it('over many samples, jittered delays stay within the ±jitterFactor window', () => {
      // Real RNG, big sample — the helper must never schedule outside
      // the documented [base*(1-jitter), base*(1+jitter)) range.
      vi.spyOn(Math, 'random').mockRestore()
      const base = 1000
      const jitterFactor = 0.2
      const backoff = createExponentialBackoff<number>({ initialMs: base, maxMs: 10_000, jitterFactor })
      let min = Infinity
      let max = -Infinity
      for (let i = 0; i < 500; i++) {
        const d = backoff.schedule(i, () => {})!
        if (d < min)
          min = d
        if (d > max)
          max = d
      }
      expect(min).toBeGreaterThanOrEqual(base * (1 - jitterFactor))
      expect(max).toBeLessThan(base * (1 + jitterFactor))
      // Sanity: actually saw a spread, not stuck on one value.
      expect(max - min).toBeGreaterThan(base * jitterFactor * 0.5)
    })

    it('never schedules a negative delay even with the highest legal jitter factor', () => {
      vi.spyOn(Math, 'random').mockReturnValue(0)
      // jitterFactor approaching (but never reaching) 1 — base*(1-jitter)
      // approaches 0 but must stay non-negative.
      const backoff = createExponentialBackoff<string>({ initialMs: 100, maxMs: 1000, jitterFactor: 0.9 })
      const delay = backoff.schedule('k', () => {})!
      expect(delay).toBeGreaterThanOrEqual(0)
    })
  })
})
