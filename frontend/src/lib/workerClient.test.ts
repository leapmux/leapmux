import { describe, expect, it, vi } from 'vitest'
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
    this.onmessage?.({ data: { id, value } } as MessageEvent)
  }
}

interface Req { id: number, payload: string }

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
    worker.reply(worker.messages[0].id, 'RESULT')
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
    worker.reply(worker.messages[1].id, 'B')
    worker.reply(worker.messages[0].id, 'A')
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
    workers[0].onerror?.() // crash
    await expect(first).resolves.toBeNull()
    expect(workers[0].terminate).toHaveBeenCalledTimes(1)

    // Next request respawns a fresh worker and works normally.
    const second = client.request(id => ({ id, payload: 'b' }))
    expect(workers).toHaveLength(2)
    workers[1].reply(workers[1].messages[0].id, 'B')
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
    workers[0].onerror?.() // crash worker0 -> first resolves null, worker0 replaced
    await expect(first).resolves.toBeNull()

    const second = client.request(id => ({ id, payload: 'b' })) // spawns worker1
    let settled = false
    void second.then(() => {
      settled = true
    })
    workers[0].onerror?.() // STALE crash from the dead worker0: must not touch worker1's pending
    await Promise.resolve()
    expect(settled).toBe(false)

    workers[1].reply(workers[1].messages[0].id, 'B')
    await expect(second).resolves.toBe('B')
  })

  it('ignores a reply for an unknown id without throwing', async () => {
    const worker = new FakeWorker()
    const client = makeClient(() => worker as unknown as Worker)
    const p = client.request(id => ({ id, payload: 'a' }))

    // A reply for an id that was never dispatched (or already settled) is a no-op.
    expect(() => worker.reply(9999, 'STALE')).not.toThrow()

    // The real reply still resolves the pending request.
    worker.reply(worker.messages[0].id, 'REAL')
    await expect(p).resolves.toBe('REAL')
  })
})
