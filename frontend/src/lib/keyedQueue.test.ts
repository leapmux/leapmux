import { describe, expect, it, vi } from 'vitest'
import { createKeyedQueue } from './keyedQueue'

/** A promise plus its resolvers, for pinning completion order. */
function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  // Asserted by the test that arms it; without this a rejection scheduled
  // before its `await` trips vitest's unhandled-rejection check.
  promise.catch(() => {})
  return { promise, resolve, reject }
}

/**
 * Let the microtasks that are already queued run.
 *
 * The chain hands one call to the next over several `then` hops, so one
 * `await` can resume the test before the next call starts. Four turns is
 * more than the chain needs, and this waits on no timer.
 */
async function flush(): Promise<void> {
  for (let i = 0; i < 4; i++)
    await Promise.resolve()
}

describe('createKeyedQueue', () => {
  it('runs the first call for a key at once', async () => {
    const queue = createKeyedQueue()
    await expect(queue.run('a', async () => 'done')).resolves.toBe('done')
  })

  // The rule the helper exists for: the SECOND call must not reach the
  // server while the first is still in flight, whatever order the replies
  // would have come back in.
  it('holds a second call for one key until the first settles', async () => {
    const queue = createKeyedQueue()
    const first = deferred<string>()
    const started: string[] = []

    const a = queue.run('k', () => {
      started.push('a')
      return first.promise
    })
    const b = queue.run('k', async () => {
      started.push('b')
      return 'b'
    })

    await Promise.resolve()
    expect(started).toEqual(['a'])

    first.resolve('a')
    await expect(a).resolves.toBe('a')
    await expect(b).resolves.toBe('b')
    expect(started).toEqual(['a', 'b'])
  })

  it('issues calls for different keys without waiting on each other', async () => {
    const queue = createKeyedQueue()
    const held = deferred<string>()
    const started: string[] = []

    const a = queue.run('a', () => {
      started.push('a')
      return held.promise
    })
    const b = queue.run('b', async () => {
      started.push('b')
      return 'b'
    })

    await expect(b).resolves.toBe('b')
    expect(started).toEqual(['a', 'b'])
    held.resolve('a')
    await a
  })

  // A refused call must not stall its key forever: the user's next edit is
  // the one thing that can repair the failure they just saw.
  it('runs the next call after the previous one rejects', async () => {
    const queue = createKeyedQueue()
    const first = deferred<string>()
    const second = vi.fn(async () => 'second')

    const a = queue.run('k', () => first.promise)
    const b = queue.run('k', second)

    await Promise.resolve()
    expect(second).not.toHaveBeenCalled()

    first.reject(new Error('refused'))
    await expect(a).rejects.toThrow('refused')
    await expect(b).resolves.toBe('second')
  })

  it('rejects the caller that made the failed call, not the one behind it', async () => {
    const queue = createKeyedQueue()
    const a = queue.run('k', async () => {
      throw new Error('mine')
    })
    const b = queue.run('k', async () => 'theirs')
    await expect(a).rejects.toThrow('mine')
    await expect(b).resolves.toBe('theirs')
  })

  // A callback that THROWS is not the same as one that returns a rejected
  // promise, and the two positions in the chain must still answer it the
  // same way. The first call for a key runs bare rather than from inside a
  // `.then`, so the queue catches the throw itself; otherwise it escapes
  // `queue.run` and the caller holding a `.catch` never sees it.
  it('turns a synchronous throw into a rejection for the first call on a key', async () => {
    const queue = createKeyedQueue()
    const sync = () => {
      throw new Error('sync refusal')
    }
    let run: Promise<never> | undefined
    expect(() => {
      run = queue.run('k', sync)
    }).not.toThrow()
    await expect(run).rejects.toThrow('sync refusal')
    // The refusal must not stall the key any more than a rejection does.
    await expect(queue.run('k', async () => 'after')).resolves.toBe('after')
  })

  // The calls queued behind a first call that throws synchronously still
  // belong to the key. They run, in order, once the throw is recorded.
  it('runs the calls queued behind a first call that throws synchronously', async () => {
    const queue = createKeyedQueue()
    const order: string[] = []
    const a = queue.run('k', () => {
      order.push('a')
      throw new Error('sync refusal')
    })
    const b = queue.run('k', async () => {
      order.push('b')
      return 'b'
    })
    const c = queue.run('k', async () => {
      order.push('c')
      return 'c'
    })

    await expect(a).rejects.toThrow('sync refusal')
    await expect(b).resolves.toBe('b')
    await expect(c).resolves.toBe('c')
    expect(order).toEqual(['a', 'b', 'c'])
  })

  // The same callback in the QUEUED position takes the other path:
  // `pending.then(call, call)` catches the throw and hands the caller a
  // rejected promise instead. Pinned because the two positions answer
  // differently for one input.
  it('turns a synchronous throw into a rejection for a queued call', async () => {
    const queue = createKeyedQueue()
    const first = deferred<string>()
    const sync = () => {
      throw new Error('sync refusal')
    }

    const a = queue.run('k', () => first.promise)
    const b = queue.run('k', sync)

    first.resolve('a')
    await expect(a).resolves.toBe('a')
    await expect(b).rejects.toThrow('sync refusal')
    // The throw must not stall the key any more than a rejection does.
    await expect(queue.run('k', async () => 'after')).resolves.toBe('after')
  })

  // A burst of clicks queues more than one call behind the one in flight,
  // which is the exact scenario the settings store and PreferencesContext
  // cite for their reply guards. The chain must stay one deep in EXECUTION
  // however deep it is in waiting.
  it('runs a burst of three calls for one key in issue order', async () => {
    const queue = createKeyedQueue()
    const first = deferred<void>()
    const second = deferred<void>()
    const started: number[] = []

    const a = queue.run('k', () => {
      started.push(1)
      return first.promise
    })
    const b = queue.run('k', () => {
      started.push(2)
      return second.promise
    })
    const c = queue.run('k', async () => {
      started.push(3)
    })

    await flush()
    expect(started).toEqual([1])

    first.resolve()
    await a
    await flush()
    // The THIRD call waits behind the second, not beside it.
    expect(started).toEqual([1, 2])

    second.resolve()
    await b
    await c
    expect(started).toEqual([1, 2, 3])
  })

  // `''` is an ordinary key here. `~/lib/keyedSeq` spends it as the
  // sentinel for a caller that passes no key, and this helper must not
  // copy that: a caller that derives a key from an empty string gets its
  // own chain, not a shared one.
  it('gives the empty-string key a chain of its own', async () => {
    const queue = createKeyedQueue()
    const held = deferred<void>()
    const started: string[] = []

    const first = queue.run('', () => {
      started.push('empty-1')
      return held.promise
    })
    const queued = queue.run('', async () => {
      started.push('empty-2')
    })
    const other = queue.run('k', async () => {
      started.push('k')
    })

    // The named key runs at once, and the second call on `''` waits its
    // turn exactly as a named key's second call would.
    await other
    await flush()
    expect(started).toEqual(['empty-1', 'k'])

    held.resolve()
    await first
    await queued
    expect(started).toEqual(['empty-1', 'k', 'empty-2'])
  })

  // A key that drained must keep serving. The chain entry for it is
  // dropped so a long session does not accumulate one promise per key it
  // ever wrote, and the drop must not lose the key's ordering.
  it('keeps ordering across a key that already drained', async () => {
    const queue = createKeyedQueue()
    const order: number[] = []
    await queue.run('k', async () => {
      order.push(1)
    })

    const held = deferred<void>()
    const a = queue.run('k', () => {
      order.push(2)
      return held.promise
    })
    const b = queue.run('k', async () => {
      order.push(3)
    })
    await Promise.resolve()
    expect(order).toEqual([1, 2])

    held.resolve()
    await a
    await b
    expect(order).toEqual([1, 2, 3])
  })
})
