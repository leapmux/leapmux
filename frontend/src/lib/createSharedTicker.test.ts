import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createSharedTicker } from '~/lib/createSharedTicker'
import { flush } from '~/test-support/async'

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

describe('createSharedTicker', () => {
  it('runs one interval for two subscribers, and both observe the tick', async () => {
    const setInterval = vi.spyOn(globalThis, 'setInterval')
    const ticker = createSharedTicker(1000)

    await createRoot(async (dispose) => {
      ticker.subscribe()
      ticker.subscribe()
      await flush()

      expect(setInterval).toHaveBeenCalledTimes(1)
      const before = ticker.tick()
      vi.advanceTimersByTime(1000)
      expect(ticker.tick()).toBe(before + 1)

      dispose()
    })
  })

  it('starts no interval until something subscribes', async () => {
    const setInterval = vi.spyOn(globalThis, 'setInterval')
    createSharedTicker(1000)
    await flush()
    expect(setInterval).not.toHaveBeenCalled()
  })

  it('clears the interval when the last subscriber goes away', async () => {
    const clearInterval = vi.spyOn(globalThis, 'clearInterval')
    const ticker = createSharedTicker(1000)

    const disposeA = await new Promise<() => void>((resolve) => {
      createRoot((d) => {
        ticker.subscribe()
        resolve(d)
      })
    })
    const disposeB = await new Promise<() => void>((resolve) => {
      createRoot((d) => {
        ticker.subscribe()
        resolve(d)
      })
    })
    await flush()

    disposeA()
    expect(clearInterval, 'one subscriber remains').not.toHaveBeenCalled()
    disposeB()
    expect(clearInterval, 'the last one clears it').toHaveBeenCalled()
  })

  /**
   * The refcount's sharp edge. Solid runs `onCleanup` whenever it disposes the
   * owner, INCLUDING before the effect queue ever flushes `onMount`, so an
   * unpaired decrement drives the counter negative -- after which `=== 1` never
   * matches again and nothing gets a timer for the rest of the session.
   */
  it('survives an owner disposed before its mount effect ever ran', async () => {
    const setInterval = vi.spyOn(globalThis, 'setInterval')
    const ticker = createSharedTicker(1000)

    // Dispose synchronously, before the effect queue flushes.
    createRoot((dispose) => {
      ticker.subscribe()
      dispose()
    })
    await flush()
    expect(setInterval, 'the aborted subscribe must not start a timer').not.toHaveBeenCalled()

    await createRoot(async (dispose) => {
      ticker.subscribe()
      await flush()
      // Would fail if the aborted subscribe had driven the counter to -1.
      expect(setInterval).toHaveBeenCalledTimes(1)
      const before = ticker.tick()
      vi.advanceTimersByTime(1000)
      expect(ticker.tick()).toBe(before + 1)
      dispose()
    })
  })

  /**
   * The one risk the parameterization adds that a module-level singleton could
   * not express: two cadences must not share a counter or a timer.
   */
  it('keeps two tickers with different intervals independent', async () => {
    const fast = createSharedTicker(1000)
    const slow = createSharedTicker(5000)

    await createRoot(async (dispose) => {
      fast.subscribe()
      slow.subscribe()
      await flush()

      const fastBefore = fast.tick()
      const slowBefore = slow.tick()

      vi.advanceTimersByTime(1000)
      expect(fast.tick(), 'the fast ticker advanced').toBe(fastBefore + 1)
      expect(slow.tick(), 'the slow ticker did not').toBe(slowBefore)

      vi.advanceTimersByTime(4000)
      expect(fast.tick()).toBe(fastBefore + 5)
      expect(slow.tick()).toBe(slowBefore + 1)

      dispose()
    })
  })

  it('restarts after every subscriber leaves and a new one arrives', async () => {
    const ticker = createSharedTicker(1000)

    const dispose = await new Promise<() => void>((resolve) => {
      createRoot((d) => {
        ticker.subscribe()
        resolve(d)
      })
    })
    await flush()
    dispose()

    await createRoot(async (d) => {
      ticker.subscribe()
      await flush()
      const before = ticker.tick()
      vi.advanceTimersByTime(1000)
      expect(ticker.tick()).toBe(before + 1)
      d()
    })
  })
})
