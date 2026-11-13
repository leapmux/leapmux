import { afterEach, describe, expect, it, vi } from 'vitest'
import { CapturingWorker } from '~/test-support/fakeWorker'
import { createWorkerClient, FAILED_START_LIMIT, WORKER_COLD_START_ALLOWANCE_MS } from './workerClient'

interface Req { id: number, payload: string }

/** A controllable fake worker: records posted messages, exposes onmessage/onerror to drive. */
class FakeWorker extends CapturingWorker<Req> {
  /** Payloads whose post throws, as the post of a message that cannot be cloned does. */
  refusedPayloads = new Set<string>()

  override postMessage(message: Req): void {
    if (this.refusedPayloads.has(message.payload))
      throw new DOMException('The message could not be cloned.', 'DataCloneError')
    super.postMessage(message)
  }

  /** Reply to the message at `index` of the posted messages. */
  replyAt(index: number, value: string): void {
    this.reply({ id: captured(this.messages, index).id, value })
  }
}

// Worker/message lookup: each test creates exactly the workers and messages it
// expects, so a missing entry is a harness failure, not a case to guard.
function captured<T>(list: readonly T[], index: number): T {
  const value = list[index]
  if (value === undefined)
    throw new Error(`expected a captured entry at index ${index}`)
  return value
}

/** Build a client over `spawn`; `extract` maps `{id, value}` verbatim. */
function makeClient(spawn: () => Worker) {
  return createWorkerClient<Req, string | null>({
    spawn,
    extract: (data: { id: number, value: string }) => ({ id: data.id, value: data.value }),
    failureValue: null,
  })
}

interface Fakes {
  workers: FakeWorker[]
  client: ReturnType<typeof makeClient>
}

/**
 * Build a client whose every spawn creates a new FakeWorker, recorded in
 * `workers`. `configure` adjusts each worker before the client posts to it.
 */
function spawnFakes(configure?: (worker: FakeWorker, index: number) => void): Fakes {
  const workers: FakeWorker[] = []
  const client = makeClient(() => {
    const w = new FakeWorker()
    configure?.(w, workers.length)
    workers.push(w)
    return w as unknown as Worker
  })
  return { workers, client }
}

/** Resolve after every queued microtask, so that a settled request has run its `then`. */
async function flushMicrotasks(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
}

/** Track whether `promise` has settled, to assert that a request is still pending. */
function settledFlag(promise: Promise<unknown>): () => boolean {
  let settled = false
  void promise.then(() => {
    settled = true
  })
  return () => settled
}

describe('createWorkerClient', () => {
  it('dispatches a request and resolves with the extracted reply value, keyed by id', async () => {
    const worker = new FakeWorker()
    const client = makeClient(() => worker as unknown as Worker)

    const p = client.request(id => ({ id, payload: 'a' }))
    expect(worker.messages).toHaveLength(1)
    worker.replyAt(0, 'RESULT')
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
    worker.replyAt(1, 'B')
    worker.replyAt(0, 'A')
    await expect(p1).resolves.toBe('A')
    await expect(p2).resolves.toBe('B')
  })

  it('resolves the failure value when spawning the worker throws', async () => {
    const client = makeClient(() => {
      throw new Error('blocked by CSP')
    })
    await expect(client.request(id => ({ id, payload: 'a' }))).resolves.toBeNull()
  })

  it('resolves only the request whose post throws to the failure value, and keeps the worker', async () => {
    // A post throws only when the message cannot be cloned, before it reaches
    // the worker, so the worker and its other requests are unaffected.
    const { workers, client } = spawnFakes((worker) => {
      worker.refusedPayloads.add('uncloneable')
    })
    const first = client.request(id => ({ id, payload: 'a' }))
    const worker = captured(workers, 0)

    const failed = client.request(id => ({ id, payload: 'uncloneable' }))
    const third = client.request(id => ({ id, payload: 'c' }))

    await expect(failed).resolves.toBeNull()
    expect(worker.terminate).not.toHaveBeenCalled()
    expect(worker.messages.map(message => message.payload)).toEqual(['a', 'c'])
    worker.replyAt(0, 'A')
    worker.replyAt(1, 'C')
    await expect(first).resolves.toBe('A')
    await expect(third).resolves.toBe('C')
    expect(workers).toHaveLength(1)
  })

  it('resolves only the request whose message cannot be built to the failure value', async () => {
    const { workers, client } = spawnFakes()
    const first = client.request(id => ({ id, payload: 'a' }))

    const failed = client.request(() => {
      throw new Error('cannot build')
    })

    await expect(failed).resolves.toBeNull()
    const worker = captured(workers, 0)
    expect(worker.terminate).not.toHaveBeenCalled()
    worker.replyAt(0, 'A')
    await expect(first).resolves.toBe('A')
  })

  it('resolves the request in progress to the failure value on a crash, and respawns on the next request', async () => {
    const { workers, client } = spawnFakes()

    const first = client.request(id => ({ id, payload: 'a' }))
    captured(workers, 0).onerror?.() // crash
    await expect(first).resolves.toBeNull()
    expect(captured(workers, 0).terminate).toHaveBeenCalledTimes(1)
    // Nothing else was pending, so no worker spawns until the next request.
    expect(workers).toHaveLength(1)

    const second = client.request(id => ({ id, payload: 'b' }))
    expect(workers).toHaveLength(2)
    captured(workers, 1).replyAt(0, 'B')
    await expect(second).resolves.toBe('B')
  })

  it('fails only the request in progress on a crash, and posts the rest again to a fresh worker', async () => {
    const { workers, client } = spawnFakes()
    const warmUp = client.request(id => ({ id, payload: 'warm-up' }))
    const crashing = client.request(id => ({ id, payload: 'crashing' }))
    const second = client.request(id => ({ id, payload: 'b' }))
    const third = client.request(id => ({ id, payload: 'c' }))
    const crashed = captured(workers, 0)
    const original = crashed.messages
    crashed.replyAt(0, 'W')
    await expect(warmUp).resolves.toBe('W')

    crashed.onerror?.()

    await expect(crashing).resolves.toBeNull()
    expect(crashed.terminate).toHaveBeenCalledOnce()
    const fresh = captured(workers, 1)
    expect(fresh.messages).toEqual([original[2], original[3]])
    fresh.replyAt(0, 'B')
    fresh.replyAt(1, 'C')
    await expect(second).resolves.toBe('B')
    await expect(third).resolves.toBe('C')
  })

  it('posts the rest again after a worker crashes before it answers anything', async () => {
    // A worker that fails to load crashes before any reply. One such crash may be
    // a passing network failure, so the requests behind the first get a fresh worker.
    const { workers, client } = spawnFakes()
    const first = client.request(id => ({ id, payload: 'a' }))
    const second = client.request(id => ({ id, payload: 'b' }))

    captured(workers, 0).onerror?.()

    await expect(first).resolves.toBeNull()
    captured(workers, 1).replyAt(0, 'B')
    await expect(second).resolves.toBe('B')
  })

  it('fails every pending request when FAILED_START_LIMIT workers in a row crash before they answer', async () => {
    const { workers, client } = spawnFakes()
    const requests = Array.from({ length: FAILED_START_LIMIT + 3 }, (_, index) =>
      client.request(id => ({ id, payload: `r${index}` })))

    for (let index = 0; index < FAILED_START_LIMIT; index++)
      captured(workers, index).onerror?.()

    const results = await Promise.all(requests)
    expect(results).toEqual(requests.map(() => null))
    // One spawn for each failed start, and none after the limit.
    expect(workers).toHaveLength(FAILED_START_LIMIT)
    for (const worker of workers)
      expect(worker.terminate).toHaveBeenCalledOnce()
  })

  it('spawns once for a later batch while workers keep failing to start, and fails the whole batch', async () => {
    const { workers, client } = spawnFakes()
    const early = Array.from({ length: FAILED_START_LIMIT }, (_, index) =>
      client.request(id => ({ id, payload: `early ${index}` })))
    for (let index = 0; index < FAILED_START_LIMIT; index++)
      captured(workers, index).onerror?.()
    await expect(Promise.all(early)).resolves.toEqual(early.map(() => null))

    const later = client.request(id => ({ id, payload: 'later' }))
    const alsoLater = client.request(id => ({ id, payload: 'also later' }))
    expect(workers).toHaveLength(FAILED_START_LIMIT + 1)
    captured(workers, FAILED_START_LIMIT).onerror?.()

    await expect(later).resolves.toBeNull()
    await expect(alsoLater).resolves.toBeNull()
    expect(workers).toHaveLength(FAILED_START_LIMIT + 1)
  })

  it('counts failed starts only in a row: a reply resets the count', async () => {
    const { workers, client } = spawnFakes()
    const requests = Array.from({ length: FAILED_START_LIMIT + 3 }, (_, index) =>
      client.request(id => ({ id, payload: `r${index}` })))

    // FAILED_START_LIMIT - 1 failed starts: one short of the limit.
    for (let index = 0; index < FAILED_START_LIMIT - 1; index++)
      captured(workers, index).onerror?.()
    // The next worker starts: it answers one request, then crashes on the next.
    const started = captured(workers, FAILED_START_LIMIT - 1)
    started.replyAt(0, 'answered')
    started.onerror?.()
    // One more failed start: the count began again at the reply, so the client
    // still posts the rest again.
    captured(workers, FAILED_START_LIMIT).onerror?.()

    const last = captured(workers, FAILED_START_LIMIT + 1)
    expect(last.messages.map(message => message.payload)).toEqual([`r${FAILED_START_LIMIT + 2}`])
    last.replyAt(0, 'last')
    await expect(requests.at(-1)).resolves.toBe('last')
  })

  // A terminated worker can still deliver a reply that it queued before it
  // stopped. That reply proves nothing about the worker that replaced it.
  it('keeps counting failed starts across a reply from a worker that a crash replaced', async () => {
    expect(FAILED_START_LIMIT).toBe(2)
    const { workers, client } = spawnFakes()
    const first = client.request(id => ({ id, payload: 'a' }))
    const second = client.request(id => ({ id, payload: 'b' }))
    const third = client.request(id => ({ id, payload: 'c' }))
    const crashed = captured(workers, 0)
    crashed.onerror?.()
    await expect(first).resolves.toBeNull()

    crashed.replyAt(1, 'STALE')
    captured(workers, 1).onerror?.()

    // The second crash before any reply reached the limit, so no third worker
    // spawned, and the crash failed every request.
    expect(workers).toHaveLength(2)
    await expect(second).resolves.toBeNull()
    await expect(third).resolves.toBeNull()
  })

  it('ignores a stale crash from an already-replaced worker (keeps live pending)', async () => {
    const { workers, client } = spawnFakes()

    const first = client.request(id => ({ id, payload: 'a' }))
    captured(workers, 0).onerror?.() // crash worker0 -> first resolves null, worker0 replaced
    await expect(first).resolves.toBeNull()

    const second = client.request(id => ({ id, payload: 'b' })) // spawns worker1
    const settled = settledFlag(second)
    captured(workers, 0).onerror?.() // STALE crash from the dead worker0: must not touch worker1's pending
    await flushMicrotasks()
    expect(settled()).toBe(false)
    expect(captured(workers, 1).terminate).not.toHaveBeenCalled()

    captured(workers, 1).replyAt(0, 'B')
    await expect(second).resolves.toBe('B')
  })

  it('ignores a reply for an unknown id without throwing', async () => {
    const worker = new FakeWorker()
    const client = makeClient(() => worker as unknown as Worker)
    const p = client.request(id => ({ id, payload: 'a' }))

    // A reply for an id that was never dispatched (or already settled) is a no-op.
    expect(() => worker.reply({ id: 9999, value: 'STALE' })).not.toThrow()

    // The real reply still resolves the pending request.
    worker.replyAt(0, 'REAL')
    await expect(p).resolves.toBe('REAL')
  })
})

describe('createWorkerClient time budget', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  /**
   * Spawn the fake workers, and let the first one answer a request, so that the
   * budgets of the test run without the cold-start allowance.
   */
  async function spawnWarmFakes(configure?: (worker: FakeWorker, index: number) => void): Promise<Fakes> {
    const fakes = spawnFakes(configure)
    const warmUp = fakes.client.request(id => ({ id, payload: 'warm-up' }))
    const worker = captured(fakes.workers, 0)
    worker.replyAt(0, 'W')
    await warmUp
    worker.messages = []
    return fakes
  }

  it('resolves a request that runs past its budget to the failure value, and terminates the worker', async () => {
    vi.useFakeTimers()
    const { workers, client } = await spawnWarmFakes()

    const stuck = client.request(id => ({ id, payload: 'nested' }), { timeBudgetMs: 1_000 })
    vi.advanceTimersByTime(999)
    expect(captured(workers, 0).terminate).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)

    await expect(stuck).resolves.toBeNull()
    expect(captured(workers, 0).terminate).toHaveBeenCalledOnce()
  })

  it('posts every other pending request again, in order and under its id, to a fresh worker', async () => {
    vi.useFakeTimers()
    const { workers, client } = await spawnWarmFakes()

    const stuck = client.request(id => ({ id, payload: 'nested' }), { timeBudgetMs: 1_000 })
    const second = client.request(id => ({ id, payload: 'b' }), { timeBudgetMs: 1_000 })
    const third = client.request(id => ({ id, payload: 'c' }))
    const original = captured(workers, 0).messages
    vi.advanceTimersByTime(1_000)
    await expect(stuck).resolves.toBeNull()

    const fresh = captured(workers, 1)
    expect(fresh.messages).toEqual([original[1], original[2]])
    fresh.replyAt(0, 'B')
    fresh.replyAt(1, 'C')
    await expect(second).resolves.toBe('B')
    await expect(third).resolves.toBe('C')
  })

  it('charges a request only from the moment it reaches the front of the queue', async () => {
    vi.useFakeTimers()
    const { workers, client } = await spawnWarmFakes()
    const first = client.request(id => ({ id, payload: 'a' }), { timeBudgetMs: 1_000 })
    const second = client.request(id => ({ id, payload: 'b' }), { timeBudgetMs: 1_000 })
    const worker = captured(workers, 0)

    vi.advanceTimersByTime(900)
    worker.replyAt(0, 'A')
    await expect(first).resolves.toBe('A')
    // The second waited 900 ms behind the first; that time is not its own.
    vi.advanceTimersByTime(999)
    worker.replyAt(1, 'B')

    await expect(second).resolves.toBe('B')
    expect(worker.terminate).not.toHaveBeenCalled()
    expect(workers).toHaveLength(1)
  })

  it('clears the budget when the request answers in time', async () => {
    vi.useFakeTimers()
    const { workers, client } = spawnFakes()
    const request = client.request(id => ({ id, payload: 'a' }), { timeBudgetMs: 1_000 })
    captured(workers, 0).replyAt(0, 'A')
    await expect(request).resolves.toBe('A')

    expect(vi.getTimerCount()).toBe(0)
  })

  it('arms no timer for a request without a budget, on a cold worker too', () => {
    vi.useFakeTimers()
    const { client } = spawnFakes()
    void client.request(id => ({ id, payload: 'a' }))

    expect(vi.getTimerCount()).toBe(0)
  })

  it('ignores a reply that the terminated worker sends after its replacement took over', async () => {
    vi.useFakeTimers()
    const { workers, client } = await spawnWarmFakes()
    const stuck = client.request(id => ({ id, payload: 'nested' }), { timeBudgetMs: 1_000 })
    const second = client.request(id => ({ id, payload: 'b' }))
    const secondId = captured(captured(workers, 0).messages, 1).id
    vi.advanceTimersByTime(1_000)
    await expect(stuck).resolves.toBeNull()

    const settled = settledFlag(second)
    captured(workers, 0).reply({ id: secondId, value: 'STALE' })
    await flushMicrotasks()
    expect(settled()).toBe(false)

    captured(workers, 1).reply({ id: secondId, value: 'B' })
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
    vi.advanceTimersByTime(WORKER_COLD_START_ALLOWANCE_MS + 1_000)

    await expect(stuck).resolves.toBeNull()
    await expect(second).resolves.toBeNull()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('resolves only a request whose post to the fresh worker throws, and posts the others', async () => {
    vi.useFakeTimers()
    const { workers, client } = await spawnWarmFakes((worker, index) => {
      if (index > 0)
        worker.refusedPayloads.add('unpostable')
    })
    const stuck = client.request(id => ({ id, payload: 'nested' }), { timeBudgetMs: 1_000 })
    const unpostable = client.request(id => ({ id, payload: 'unpostable' }))
    const third = client.request(id => ({ id, payload: 'c' }))
    const original = captured(workers, 0).messages

    vi.advanceTimersByTime(1_000)

    await expect(stuck).resolves.toBeNull()
    await expect(unpostable).resolves.toBeNull()
    const fresh = captured(workers, 1)
    expect(fresh.terminate).not.toHaveBeenCalled()
    expect(fresh.messages).toEqual([original[2]])
    fresh.replyAt(0, 'C')
    await expect(third).resolves.toBe('C')
  })

  it('arms the next budget for a re-posted request on the fresh worker', async () => {
    vi.useFakeTimers()
    const { workers, client } = await spawnWarmFakes()
    const stuck = client.request(id => ({ id, payload: 'nested' }), { timeBudgetMs: 1_000 })
    const alsoStuck = client.request(id => ({ id, payload: 'nested too' }), { timeBudgetMs: 2_000 })
    vi.advanceTimersByTime(1_000)
    await expect(stuck).resolves.toBeNull()

    // The fresh worker has not answered anything, so it gets the allowance too.
    vi.advanceTimersByTime(WORKER_COLD_START_ALLOWANCE_MS + 2_000)
    await expect(alsoStuck).resolves.toBeNull()
    expect(captured(workers, 1).terminate).toHaveBeenCalledOnce()
  })

  it('keeps posting the rest again when a worker runs past a budget before it answers anything', async () => {
    // A slow body is not a failed start: only a crash before a reply counts.
    vi.useFakeTimers()
    const { workers, client } = spawnFakes()
    const slowBodies = Array.from({ length: FAILED_START_LIMIT + 1 }, (_, index) =>
      client.request(id => ({ id, payload: `slow ${index}` }), { timeBudgetMs: 1_000 }))
    const valid = client.request(id => ({ id, payload: 'valid' }))

    for (let index = 0; index <= FAILED_START_LIMIT; index++)
      vi.advanceTimersByTime(WORKER_COLD_START_ALLOWANCE_MS + 1_000)

    await expect(Promise.all(slowBodies)).resolves.toEqual(slowBodies.map(() => null))
    const last = captured(workers, FAILED_START_LIMIT + 1)
    expect(last.messages.map(message => message.payload)).toEqual(['valid'])
    last.replyAt(0, 'V')
    await expect(valid).resolves.toBe('V')
  })

  it('posts the rest again when a budget runs out after FAILED_START_LIMIT crashes', async () => {
    vi.useFakeTimers()
    const { workers, client } = spawnFakes()
    const early = Array.from({ length: FAILED_START_LIMIT }, (_, index) =>
      client.request(id => ({ id, payload: `early ${index}` })))
    for (let index = 0; index < FAILED_START_LIMIT; index++)
      captured(workers, index).onerror?.()
    await expect(Promise.all(early)).resolves.toEqual(early.map(() => null))

    const slow = client.request(id => ({ id, payload: 'slow' }), { timeBudgetMs: 1_000 })
    const valid = client.request(id => ({ id, payload: 'valid' }))
    vi.advanceTimersByTime(WORKER_COLD_START_ALLOWANCE_MS + 1_000)

    await expect(slow).resolves.toBeNull()
    const fresh = captured(workers, FAILED_START_LIMIT + 1)
    expect(fresh.messages.map(message => message.payload)).toEqual(['valid'])
    fresh.replyAt(0, 'V')
    await expect(valid).resolves.toBe('V')
  })
})

describe('createWorkerClient cold start', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('adds the cold-start allowance to the budget of a request on a worker that has not answered yet', async () => {
    vi.useFakeTimers()
    const { workers, client } = spawnFakes()
    const first = client.request(id => ({ id, payload: 'a' }), { timeBudgetMs: 1_000 })

    vi.advanceTimersByTime(WORKER_COLD_START_ALLOWANCE_MS + 999)
    expect(captured(workers, 0).terminate).not.toHaveBeenCalled()
    captured(workers, 0).replyAt(0, 'A')

    await expect(first).resolves.toBe('A')
  })

  it('ends a request on a cold worker at its budget plus the allowance', async () => {
    vi.useFakeTimers()
    const { workers, client } = spawnFakes()
    const first = client.request(id => ({ id, payload: 'a' }), { timeBudgetMs: 1_000 })

    vi.advanceTimersByTime(WORKER_COLD_START_ALLOWANCE_MS + 1_000)

    await expect(first).resolves.toBeNull()
    expect(captured(workers, 0).terminate).toHaveBeenCalledOnce()
  })

  it('charges a request behind the first answer only its own budget', async () => {
    vi.useFakeTimers()
    const { workers, client } = spawnFakes()
    const first = client.request(id => ({ id, payload: 'a' }), { timeBudgetMs: 1_000 })
    const second = client.request(id => ({ id, payload: 'b' }), { timeBudgetMs: 1_000 })
    captured(workers, 0).replyAt(0, 'A')
    await expect(first).resolves.toBe('A')

    vi.advanceTimersByTime(1_000)

    await expect(second).resolves.toBeNull()
    expect(captured(workers, 0).terminate).toHaveBeenCalledOnce()
  })

  it('charges a cold request only its budget when the client sets no allowance', async () => {
    vi.useFakeTimers()
    const worker = new FakeWorker()
    const client = createWorkerClient<Req, string | null>({
      spawn: () => worker as unknown as Worker,
      extract: (data: { id: number, value: string }) => ({ id: data.id, value: data.value }),
      failureValue: null,
      coldStartAllowanceMs: 0,
    })
    const first = client.request(id => ({ id, payload: 'a' }), { timeBudgetMs: 1_000 })

    vi.advanceTimersByTime(999)
    expect(worker.terminate).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)

    await expect(first).resolves.toBeNull()
    expect(worker.terminate).toHaveBeenCalledOnce()
  })

  it('keeps the allowance of a cold request when the worker answers a later request first', async () => {
    vi.useFakeTimers()
    const { workers, client } = spawnFakes()
    const first = client.request(id => ({ id, payload: 'a' }), { timeBudgetMs: 1_000 })
    const second = client.request(id => ({ id, payload: 'b' }))
    captured(workers, 0).replyAt(1, 'B')
    await expect(second).resolves.toBe('B')

    vi.advanceTimersByTime(1_000)
    expect(captured(workers, 0).terminate).not.toHaveBeenCalled()
    captured(workers, 0).replyAt(0, 'A')

    await expect(first).resolves.toBe('A')
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
    worker.replyAt(0, 'A')
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
    const { workers, client } = spawnFakes()

    client.prepare()
    captured(workers, 0).onerror?.()
    client.prepare()

    expect(workers).toHaveLength(2)
    expect(captured(workers, 0).terminate).toHaveBeenCalledOnce()
  })
})
