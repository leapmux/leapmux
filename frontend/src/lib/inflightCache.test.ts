import { describe, expect, it, vi } from 'vitest'
import { deferred } from '~/test-support/async'
import { createInflightCache } from './inflightCache'

describe('createInflightCache', () => {
  it('returns the same promise for concurrent calls with the same key', async () => {
    const cache = createInflightCache<string, number>()
    const factory = vi.fn(async () => 42)

    const p1 = cache.run('k', factory)
    const p2 = cache.run('k', factory)

    expect(p1).toBe(p2)
    await expect(p1).resolves.toBe(42)
    expect(factory).toHaveBeenCalledTimes(1)
  })

  it('runs the factory independently for different keys', async () => {
    const cache = createInflightCache<string, string>()
    const factory = vi.fn(async (k: string) => `v-${k}`)

    const [a, b] = await Promise.all([
      cache.run('a', () => factory('a')),
      cache.run('b', () => factory('b')),
    ])

    expect(factory).toHaveBeenCalledTimes(2)
    expect(a).toBe('v-a')
    expect(b).toBe('v-b')
  })

  it('lets a new factory run after the previous one settles', async () => {
    const cache = createInflightCache<string, number>()
    const factory = vi.fn(async () => 1)

    await cache.run('k', factory)
    await cache.run('k', factory)

    expect(factory).toHaveBeenCalledTimes(2)
  })

  it('clears the pending entry after the factory rejects', async () => {
    const cache = createInflightCache<string, number>()
    const err = new Error('boom')
    await expect(cache.run('k', async () => {
      throw err
    })).rejects.toBe(err)
    expect(cache.has('k')).toBe(false)
  })

  it('preserves a synchronous factory throw and lets a retry run immediately', async () => {
    const cache = createInflightCache<string, number>()
    const err = new Error('sync boom')
    // The factory throws before it returns any promise, so the flight settles
    // inside run() itself. The original error must still surface, the entry
    // must not stay stuck on that settled promise, and a retry must invoke the
    // factory again at once rather than replaying the stale rejection.
    await expect(cache.run('k', () => {
      throw err
    })).rejects.toBe(err)
    expect(cache.has('k')).toBe(false)
    const factory = vi.fn(async () => 5)
    await expect(cache.run('k', factory)).resolves.toBe(5)
    expect(factory).toHaveBeenCalledTimes(1)
  })

  it('exposes has() for callers that want a pre-check', async () => {
    const cache = createInflightCache<string, number>()
    expect(cache.has('k')).toBe(false)

    let release!: () => void
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    const p = cache.run('k', async () => {
      await gate
      return 1
    })

    expect(cache.has('k')).toBe(true)
    release()
    await p
    expect(cache.has('k')).toBe(false)
  })

  it('clear() forgets tracked entries without cancelling in-flight work', async () => {
    const cache = createInflightCache<string, number>()
    let release!: () => void
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    const p = cache.run('k', async () => {
      await gate
      return 7
    })

    cache.clear()
    expect(cache.has('k')).toBe(false)

    // A second caller after clear() starts a fresh factory invocation even
    // though the first is still in flight.
    const replacementResult = deferred<number>()
    const factory = vi.fn(() => replacementResult.promise)
    const p2 = cache.run('k', factory)
    expect(factory).toHaveBeenCalledTimes(1)

    release()
    await expect(p).resolves.toBe(7)
    const unexpectedFactory = vi.fn(async () => 9)
    const third = cache.run('k', unexpectedFactory)
    expect(third).toBe(p2)
    expect(unexpectedFactory).not.toHaveBeenCalled()
    replacementResult.resolve(8)
    await expect(third).resolves.toBe(8)
  })
})
