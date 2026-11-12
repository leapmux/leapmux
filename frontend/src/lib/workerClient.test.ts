import { afterEach, describe, expect, it, vi } from 'vitest'
import { createWorkerClient } from './workerClient'

/** A controllable fake worker: records posted messages, exposes onmessage/onerror to drive. */
class FakeWorker {
  onmessage: ((event: MessageEvent) => void) | null = null
  onerror: (() => void) | null = null
  messages: Array<{ id: number, payload: string }> = []
  terminate = vi.fn()

  postMessage(message: { id: number, payload: string }): void {
    this.messages.push(message)
  }

  reply(id: number, value: string): void {
    this.onmessage?.({
      data: { id, value },
    } as MessageEvent)
  }
}

interface Req { id: number, payload: string }

// Worker/message lookup: each test creates exactly the workers and messages it
// expects, so a missing entry is a harness failure, not a case to guard.
function captured<T>(list: readonly T[], index: number): T {
  const value = list[index]
  if (value === undefined)
    throw new Error(`expected a captured entry at index ${index}`)
  return value
}

/** Build a client over a fresh FakeWorker list; `extract` maps `{id, value}` verbatim. */
function makeClient(spawn: () => Worker) {
  return createWorkerClient<Req, string | null>({
    spawn,
    extract: (data: { id: number, value: string }) => ({ id: data.id, value: data.value }),
    failureValue: null,
  })
}

describe('createWorkerClient', () => {
  it('dispatches a request and resolves with the extracted reply value, keyed by id', async () => {
    const worker = new FakeWorker()
    const client = makeClient(() => worker as unknown as Worker)

    const p = client.request(id => ({ id, payload: 'a' }))
    expect(worker.messages).toHaveLength(1)
    worker.reply(captured(worker.messages, 0).id, 'RESULT')
    await expect(p).resolves.toBe('RESULT')
  })

  it('lazily spawns exactly one worker shared across requests', async () => {
    const worker = new FakeWorker()
    const spawn = vi.fn(() => worker as unknown as Worker)
    const client = makeClient(spawn)

    expect(spawn).not.toHaveBeenCalled() // not spawned until first request
    const p1 = client.request(id => ({ id, payload: 'a' }))
    const p2 = client.request(id => ({ id, payload: 'b' }))
    expect(spawn).toHaveBeenCalledTimes(1)
    expect(worker.messages).toHaveLength(2)

    // Replies routed by id, even out of order.
    worker.reply(captured(worker.messages, 1).id, 'B')
    worker.reply(captured(worker.messages, 0).id, 'A')
    await expect(p1).resolves.toBe('A')
    await expect(p2).resolves.toBe('B')
  })

  it('resolves the failure value when spawning the worker throws', async () => {
    const client = makeClient(() => {
      throw new Error('blocked by CSP')
    })
    await expect(client.request(id => ({ id, payload: 'a' }))).resolves.toBeNull()
  })

  it('resolves failure + terminates + resolves other pending when postMessage throws', async () => {
    let posts = 0
    const worker = new FakeWorker()
    worker.postMessage = () => {
      posts++
      if (posts === 2)
        throw new Error('port closed')
    }
    const client = makeClient(() => worker as unknown as Worker)

    const first = client.request(id => ({ id, payload: 'a' })) // posts ok, stays pending
    const second = client.request(id => ({ id, payload: 'b' })) // post throws -> failWorker

    await expect(second).resolves.toBeNull()
    await expect(first).resolves.toBeNull() // the failing post resolves ALL pending
    expect(worker.terminate).toHaveBeenCalledTimes(1)
  })

  it('on crash resolves all pending to the failure value and respawns on the next request', async () => {
    const workers: FakeWorker[] = []
    const client = makeClient(() => {
      const w = new FakeWorker()
      workers.push(w)
      return w as unknown as Worker
    })

    const first = client.request(id => ({ id, payload: 'a' }))
    captured(workers, 0).onerror?.() // crash
    await expect(first).resolves.toBeNull()
    expect(captured(workers, 0).terminate).toHaveBeenCalledTimes(1)

    // Next request respawns a fresh worker and works normally.
    const second = client.request(id => ({ id, payload: 'b' }))
    expect(workers).toHaveLength(2)
    captured(workers, 1).reply(captured(captured(workers, 1).messages, 0).id, 'B')
    await expect(second).resolves.toBe('B')
  })

  it('ignores a stale crash from an already-replaced worker (keeps live pending)', async () => {
    const workers: FakeWorker[] = []
    const client = makeClient(() => {
      const w = new FakeWorker()
      workers.push(w)
      return w as unknown as Worker
    })

    const first = client.request(id => ({ id, payload: 'a' }))
    captured(workers, 0).onerror?.() // crash worker0 -> first resolves null, worker0 replaced
    await expect(first).resolves.toBeNull()

    const second = client.request(id => ({ id, payload: 'b' })) // spawns worker1
    let settled = false
    void second.then(() => {
      settled = true
    })
    captured(workers, 0).onerror?.() // STALE crash from the dead worker0: must not touch worker1's pending
    await Promise.resolve()
    expect(settled).toBe(false)

    captured(workers, 1).reply(captured(captured(workers, 1).messages, 0).id, 'B')
    await expect(second).resolves.toBe('B')
  })

  it('ignores a reply for an unknown id without throwing', async () => {
    const worker = new FakeWorker()
    const client = makeClient(() => worker as unknown as Worker)
    const p = client.request(id => ({ id, payload: 'a' }))

    // A reply for an id that was never dispatched (or already settled) is a no-op.
    expect(() => worker.reply(9999, 'STALE')).not.toThrow()

    // The real reply still resolves the pending request.
    worker.reply(captured(worker.messages, 0).id, 'REAL')
    await expect(p).resolves.toBe('REAL')
  })
})

describe('createWorkerClient time budget', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  function spawnFakes(): { workers: FakeWorker[], client: ReturnType<typeof makeClient> } {
    const workers: FakeWorker[] = []
    const client = makeClient(() => {
      const w = new FakeWorker()
      workers.push(w)
      return w as unknown as Worker
    })
    return { workers, client }
  }

  it('resolves a request that runs past its budget to the failure value, and terminates the worker', async () => {
    vi.useFakeTimers()
    const { workers, client } = spawnFakes()

    const stuck = client.request(id => ({ id, payload: 'nested' }), { timeBudgetMs: 1_000 })
    vi.advanceTimersByTime(999)
    expect(captured(workers, 0).terminate).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)

    await expect(stuck).resolves.toBeNull()
    expect(captured(workers, 0).terminate).toHaveBeenCalledOnce()
  })

  it('posts every other pending request again, in order and under its id, to a fresh worker', async () => {
    vi.useFakeTimers()
    const { workers, client } = spawnFakes()

    const stuck = client.request(id => ({ id, payload: 'nested' }), { timeBudgetMs: 1_000 })
    const second = client.request(id => ({ id, payload: 'b' }), { timeBudgetMs: 1_000 })
    const third = client.request(id => ({ id, payload: 'c' }))
    const original = captured(workers, 0).messages
    vi.advanceTimersByTime(1_000)
    await expect(stuck).resolves.toBeNull()

    const fresh = captured(workers, 1)
    expect(fresh.messages).toEqual([original[1], original[2]])
    fresh.reply(captured(original, 1).id, 'B')
    fresh.reply(captured(original, 2).id, 'C')
    await expect(second).resolves.toBe('B')
    await expect(third).resolves.toBe('C')
  })

  it('charges a request only from the moment it reaches the front of the queue', async () => {
    vi.useFakeTimers()
    const { workers, client } = spawnFakes()
    const first = client.request(id => ({ id, payload: 'a' }), { timeBudgetMs: 1_000 })
    const second = client.request(id => ({ id, payload: 'b' }), { timeBudgetMs: 1_000 })
    const worker = captured(workers, 0)

    vi.advanceTimersByTime(900)
    worker.reply(captured(worker.messages, 0).id, 'A')
    await expect(first).resolves.toBe('A')
    // The second waited 900 ms behind the first; that time is not its own.
    vi.advanceTimersByTime(999)
    worker.reply(captured(worker.messages, 1).id, 'B')

    await expect(second).resolves.toBe('B')
    expect(worker.terminate).not.toHaveBeenCalled()
    expect(workers).toHaveLength(1)
  })

  it('clears the budget when the request answers in time', async () => {
    vi.useFakeTimers()
    const { workers, client } = spawnFakes()
    const request = client.request(id => ({ id, payload: 'a' }), { timeBudgetMs: 1_000 })
    captured(workers, 0).reply(captured(captured(workers, 0).messages, 0).id, 'A')
    await expect(request).resolves.toBe('A')

    expect(vi.getTimerCount()).toBe(0)
  })

  it('arms no timer for a request without a budget', () => {
    vi.useFakeTimers()
    const { client } = spawnFakes()
    void client.request(id => ({ id, payload: 'a' }))

    expect(vi.getTimerCount()).toBe(0)
  })

  it('ignores a reply that the terminated worker sends after its replacement took over', async () => {
    vi.useFakeTimers()
    const { workers, client } = spawnFakes()
    const stuck = client.request(id => ({ id, payload: 'nested' }), { timeBudgetMs: 1_000 })
    const second = client.request(id => ({ id, payload: 'b' }))
    const secondId = captured(captured(workers, 0).messages, 1).id
    vi.advanceTimersByTime(1_000)
    await expect(stuck).resolves.toBeNull()

    let settled = false
    void second.then(() => {
      settled = true
    })
    captured(workers, 0).reply(secondId, 'STALE')
    await Promise.resolve()
    expect(settled).toBe(false)

    captured(workers, 1).reply(secondId, 'B')
    await expect(second).resolves.toBe('B')
  })

  it('resolves the requests behind it to the failure value when no fresh worker can spawn', async () => {
    vi.useFakeTimers()
    let spawns = 0
    const worker = new FakeWorker()
    const client = makeClient(() => {
      spawns++
      if (spawns > 1)
        throw new Error('blocked by CSP')
      return worker as unknown as Worker
    })
    const stuck = client.request(id => ({ id, payload: 'nested' }), { timeBudgetMs: 1_000 })
    const second = client.request(id => ({ id, payload: 'b' }))
    vi.advanceTimersByTime(1_000)

    await expect(stuck).resolves.toBeNull()
    await expect(second).resolves.toBeNull()
  })

  it('arms the next budget for a re-posted request on the fresh worker', async () => {
    vi.useFakeTimers()
    const { workers, client } = spawnFakes()
    const stuck = client.request(id => ({ id, payload: 'nested' }), { timeBudgetMs: 1_000 })
    const alsoStuck = client.request(id => ({ id, payload: 'nested too' }), { timeBudgetMs: 2_000 })
    vi.advanceTimersByTime(1_000)
    await expect(stuck).resolves.toBeNull()

    vi.advanceTimersByTime(2_000)
    await expect(alsoStuck).resolves.toBeNull()
    expect(captured(workers, 1).terminate).toHaveBeenCalledOnce()
  })
})

describe('createWorkerClient prepare', () => {
  it('spawns the worker ahead of the first request, and the request reuses it', async () => {
    const worker = new FakeWorker()
    const spawn = vi.fn(() => worker as unknown as Worker)
    const client = makeClient(spawn)

    client.prepare()
    expect(spawn).toHaveBeenCalledOnce()
    client.prepare()
    const p = client.request(id => ({ id, payload: 'a' }))

    expect(spawn).toHaveBeenCalledOnce()
    worker.reply(captured(worker.messages, 0).id, 'A')
    await expect(p).resolves.toBe('A')
  })

  it('leaves a worker that cannot spawn for the first request to report', async () => {
    const spawn = vi.fn((): Worker => {
      throw new Error('blocked by CSP')
    })
    const client = makeClient(spawn)

    expect(() => client.prepare()).not.toThrow()
    await expect(client.request(id => ({ id, payload: 'a' }))).resolves.toBeNull()
    // The request tried again rather than reusing a broken worker.
    expect(spawn).toHaveBeenCalledTimes(2)
  })

  it('spawns a fresh worker after a crash', () => {
    const workers = [
      new FakeWorker(),
      new FakeWorker(),
    ]
    let spawned = 0
    const client = makeClient(() => workers[spawned++] as unknown as Worker)

    client.prepare()
    captured(workers, 0).onerror?.()
    client.prepare()

    expect(spawned).toBe(2)
    expect(captured(workers, 0).terminate).toHaveBeenCalledOnce()
  })
})
