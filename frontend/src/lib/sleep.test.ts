import { describe, expect, it, vi } from 'vitest'
import { sleep } from './sleep'

describe('sleep', () => {
  it('resolves after the delay', async () => {
    vi.useFakeTimers()
    try {
      const done = vi.fn()
      const p = sleep(50).then(done)
      expect(done).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(50)
      await p
      expect(done).toHaveBeenCalledTimes(1)
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('resolves normally when the signal never aborts', async () => {
    const controller = new AbortController()
    await expect(sleep(0, controller.signal)).resolves.toBeUndefined()
  })

  // A caller that already gave up must not wait at all. The retry loop in
  // workerRpc checks its signal between attempts, so a backoff that
  // ignored an already-aborted signal would still burn the whole delay.
  it('rejects at once with the reason when the signal is already aborted', async () => {
    const controller = new AbortController()
    controller.abort(new Error('gave up'))
    await expect(sleep(60_000, controller.signal)).rejects.toThrow('gave up')
  })

  it('rejects mid-wait when the signal aborts, and clears the timer', async () => {
    vi.useFakeTimers()
    try {
      const controller = new AbortController()
      const p = sleep(60_000, controller.signal)
      const settled = vi.fn()
      void p.catch(settled)
      controller.abort(new Error('aborted mid-backoff'))
      await expect(p).rejects.toThrow('aborted mid-backoff')
      // Nothing is left to fire: an uncleared timer would resolve a promise
      // that already rejected and keep the fake clock busy.
      expect(vi.getTimerCount()).toBe(0)
    }
    finally {
      vi.useRealTimers()
    }
  })

  it('still rejects when the signal aborts with no reason given', async () => {
    const controller = new AbortController()
    const p = sleep(60_000, controller.signal)
    controller.abort()
    await expect(p).rejects.toBeDefined()
  })
})
