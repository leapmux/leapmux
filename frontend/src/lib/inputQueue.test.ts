import { describe, expect, it, vi } from 'vitest'
import { deferred } from '~/test-support/async'
import { createSharedInputQueues } from './inputQueue'

const enc = (s: string) => new TextEncoder().encode(s)
const dec = (b: Uint8Array) => new TextDecoder().decode(b)

// A deadline no test below reaches unless it says so.
const noDeadline = () => 60_000

describe('createSharedInputQueues', () => {
  it('joins data queued while a send is in flight into one ordered batch', async () => {
    const queues = createSharedInputQueues<string, null>()
    const batches: string[] = []
    const gate = deferred<void>()
    const send = vi.fn(async (_key: string, _ctx: null, batch: Uint8Array) => {
      batches.push(dec(batch))
      if (batches.length === 1)
        await gate.promise
    })
    const drain = { resolve: () => null, send, sendDeadlineMs: noDeadline }

    const first = queues.enqueue('t-1', enc('a'), drain)
    await Promise.resolve()
    expect(send).toHaveBeenCalledTimes(1)

    void queues.enqueue('t-1', enc('b'), drain)
    void queues.enqueue('t-1', enc('c'), drain)
    await Promise.resolve()
    expect(send, 'no second send may start while one is in flight').toHaveBeenCalledTimes(1)

    gate.resolve()
    await first
    expect(send).toHaveBeenCalledTimes(2)
    expect(batches).toEqual(['a', 'bc'])
  })

  it('resolves a joining enqueue only when the owning drain finishes', async () => {
    const queues = createSharedInputQueues<string, null>()
    const gate = deferred<void>()
    const send = vi.fn(async () => {
      if (send.mock.calls.length === 1)
        await gate.promise
    })
    const drain = { resolve: () => null, send, sendDeadlineMs: noDeadline }

    const first = queues.enqueue('t-1', enc('a'), drain)
    let joinerDone = false
    const joiner = queues.enqueue('t-1', enc('b'), drain)
    joiner.then(() => {
      joinerDone = true
    })
    await Promise.resolve()

    // The joiner's bytes are still queued behind the in-flight send, so its
    // promise must not claim completion yet.
    expect(joinerDone).toBe(false)

    gate.resolve()
    await first
    await joiner
    expect(joinerDone).toBe(true)
  })

  it('drops the queue when resolve returns undefined, then a later enqueue starts fresh', async () => {
    const queues = createSharedInputQueues<string, null>()
    const batches: string[] = []
    const send = async (_key: string, _ctx: null, batch: Uint8Array) => {
      batches.push(dec(batch))
    }
    let alive = true
    const drain = { resolve: () => (alive ? null : undefined), send, sendDeadlineMs: noDeadline }

    const first = queues.enqueue('t-1', enc('a'), drain)
    alive = false // target exits mid-burst
    void queues.enqueue('t-1', enc('b'), drain)
    await first
    expect(batches).toEqual(['a']) // queued bytes dropped, queue deleted

    alive = true
    await queues.enqueue('t-1', enc('c'), drain)
    expect(batches).toEqual(['a', 'c'])
  })

  it('drops the queue when resolve throws, without rejecting into the input path', async () => {
    const queues = createSharedInputQueues<string, null>()
    const send = vi.fn(async () => {})
    const drain = {
      resolve: () => {
        throw new Error('disposed store')
      },
      send,
      sendDeadlineMs: noDeadline,
    }

    // The drain-starting promise is fire-and-forget at the call site, so a
    // throwing resolve must resolve (and drop) rather than reject.
    await expect(queues.enqueue('t-1', enc('a'), drain)).resolves.toBeUndefined()
    expect(send).not.toHaveBeenCalled()

    // The queue is gone: a later enqueue starts fresh.
    const ok = { resolve: () => null, send, sendDeadlineMs: noDeadline }
    await queues.enqueue('t-1', enc('b'), ok)
    expect(send).toHaveBeenCalledTimes(1)
  })

  it('retries a rejected batch once, merged with bytes typed meanwhile', async () => {
    const queues = createSharedInputQueues<string, null>()
    const gate = deferred<void>()
    const batches: string[] = []
    const send = vi.fn(async (_key: string, _ctx: null, batch: Uint8Array) => {
      batches.push(dec(batch))
      if (batches.length === 1) {
        await gate.promise.then(() => {
          throw new Error('worker offline')
        })
      }
    })
    const drain = { resolve: () => null, send, sendDeadlineMs: noDeadline }

    const first = queues.enqueue('t-1', enc('a'), drain)
    await Promise.resolve()
    void queues.enqueue('t-1', enc('b'), drain)
    gate.resolve()

    // The rejected batch is safe to re-send (it never ran) and goes first,
    // joined with the bytes that arrived while it was failing.
    await first
    expect(batches).toEqual(['a', 'ab'])
  })

  it('drops a batch after a second rejection and keeps draining the rest', async () => {
    const queues = createSharedInputQueues<string, null>()
    const batches: string[] = []
    const gate = deferred<void>()
    let calls = 0
    const send = async (_key: string, _ctx: null, batch: Uint8Array) => {
      calls++
      batches.push(dec(batch))
      if (calls === 1) {
        await gate.promise.then(() => {
          throw new Error('worker offline')
        })
      }
      if (calls === 2)
        throw new Error('worker offline')
    }
    const drain = { resolve: () => null, send, sendDeadlineMs: noDeadline }

    const first = queues.enqueue('t-1', enc('a'), drain)
    await Promise.resolve()
    void queues.enqueue('t-1', enc('b'), drain)
    gate.resolve()
    await first

    // The retry merged 'a' with 'b' and failed too; the batch is gone and
    // the queue is empty, so the drain ends without another send.
    expect(calls).toBe(2)
    expect(batches).toEqual(['a', 'ab'])
  })

  it('moves on at the deadline and never re-sends the abandoned batch', async () => {
    const queues = createSharedInputQueues<string, null>()
    const batches: string[] = []
    const gate = deferred<void>()
    const send = vi.fn(async (_key: string, _ctx: null, batch: Uint8Array) => {
      batches.push(dec(batch))
      if (batches.length === 1)
        await gate.promise // black-holed: never settles
    })
    const drain = { resolve: () => null, send, sendDeadlineMs: () => 0 }

    const first = queues.enqueue('t-1', enc('a'), drain)
    void queues.enqueue('t-1', enc('b'), drain)
    await new Promise(resolve => setTimeout(resolve, 10))

    // The deadline freed the drain without the first send settling; the
    // abandoned batch may still deliver, so it must not ride again.
    expect(batches).toEqual(['a', 'b'])
    gate.resolve()
    await first
    expect(batches).toEqual(['a', 'b'])
  })

  it('hands the next batch to the most recent drain', async () => {
    const queues = createSharedInputQueues<string, 'A' | 'B'>()
    const gate = deferred<void>()
    const sendA = vi.fn(async () => {
      if (sendA.mock.calls.length === 1)
        await gate.promise
    })
    const sendB = vi.fn(async (_key: string, _ctx: 'A' | 'B', _batch: Uint8Array) => {})
    const drainA = { resolve: () => 'A' as const, send: sendA, sendDeadlineMs: noDeadline }
    const drainB = { resolve: () => 'B' as const, send: sendB, sendDeadlineMs: noDeadline }

    const first = queues.enqueue('t-1', enc('a'), drainA)
    await Promise.resolve()
    // A remounted owner joins the running burst with its own drain.
    void queues.enqueue('t-1', enc('b'), drainB)
    gate.resolve()
    await first

    expect(sendA).toHaveBeenCalledTimes(1)
    expect(sendB).toHaveBeenCalledTimes(1)
    expect(dec(sendB.mock.calls[0][2])).toBe('b')
  })
})
