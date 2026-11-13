import type { FieldDigestRequest } from './fieldDigestWorker'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CapturingWorker, installCapturingWorkers } from '~/test-support/fakeWorker'
import { digestFields } from './fieldDigest'
import {
  FIELD_DIGEST_BASE_BUDGET_MS,
  FIELD_DIGEST_BUDGET_MS_PER_MIB,
  fieldDigestBudgetMs,
} from './fieldDigestClient'
import { FAILED_START_LIMIT } from './workerClient'

type ClientModule = typeof import('./fieldDigestClient')

/** Import the client afresh, so that it spawns from this test's Worker. */
async function loadClient(): Promise<ClientModule> {
  vi.resetModules()
  return import('./fieldDigestClient')
}

const encoder = new TextEncoder()

/** The `index`th worker that the client spawned. */
function spawnedWorker(workers: readonly CapturingWorker<FieldDigestRequest>[], index: number): CapturingWorker<FieldDigestRequest> {
  const worker = workers[index]
  if (worker === undefined)
    throw new Error(`expected a spawned worker at index ${index}`)
  return worker
}

/**
 * Stub the global Worker with a CapturingWorker that also records the transfer
 * list of each post, and return each one that the client spawns, in order. Call
 * `vi.unstubAllGlobals()` after the test.
 */
function installTransferRecordingWorkers() {
  const spawned: CapturingWorker<FieldDigestRequest>[] = []
  const transfers: unknown[] = []
  vi.stubGlobal('Worker', class extends CapturingWorker<FieldDigestRequest> {
    constructor() {
      super()
      spawned.push(this)
    }

    override postMessage(message: FieldDigestRequest, transfer?: unknown): void {
      transfers.push(transfer)
      super.postMessage(message)
    }
  })
  return { spawned, transfers }
}

/** The only message that `worker` received. */
function onlyMessage(worker: CapturingWorker<FieldDigestRequest>): FieldDigestRequest {
  expect(worker.messages).toHaveLength(1)
  return worker.messages[0]!
}

describe('digestFieldsOffThread', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('answers with the digest that the worker computes', async () => {
    const { digestFieldsOffThread } = await loadClient()
    const fields = [
      encoder.encode('agent-1'),
      Uint8Array.of(1, 2, 3),
    ]

    await expect(digestFieldsOffThread(fields)).resolves.toBe(digestFields(fields))
  })

  it('posts the fields with no transfer list, so the caller keeps its bytes', async () => {
    const { spawned, transfers } = installTransferRecordingWorkers()
    const { digestFieldsOffThread } = await loadClient()
    const data = Uint8Array.of(4, 5, 6)
    const fields = [
      encoder.encode('a.txt'),
      data,
    ]

    const digest = digestFieldsOffThread(fields)
    const worker = spawnedWorker(spawned, 0)
    const message = onlyMessage(worker)

    expect(message.fields.map(field => [...field])).toEqual(fields.map(field => [...field]))
    // The request is cloned, never transferred: the caller still sends the bytes,
    // and a crash posts the request again to a fresh worker.
    expect(transfers).toEqual([undefined])
    expect(data.byteLength).toBe(3)
    worker.reply({ id: message.id, digest: 'from-the-worker' })
    await expect(digest).resolves.toBe('from-the-worker')
  })

  it('posts a subarray field as a copy that covers its whole buffer', async () => {
    const workers = installCapturingWorkers<FieldDigestRequest>()
    const { digestFieldsOffThread } = await loadClient()
    const whole = Uint8Array.of(1, 2, 3, 4, 5, 6, 7, 8)
    const view = whole.subarray(2, 5)

    const digest = digestFieldsOffThread([view])
    const worker = spawnedWorker(workers, 0)
    const message = onlyMessage(worker)
    const [posted] = message.fields

    expect(posted!.buffer.byteLength).toBe(view.byteLength)
    expect([...posted!]).toEqual([3, 4, 5])
    worker.reply({ id: message.id, digest: 'from-the-worker' })
    await digest
  })

  // A worker that crashes before it answers anything most likely cannot load its
  // script. The first crash fails the request in progress and moves the other to
  // a fresh worker; the crash that reaches FAILED_START_LIMIT fails them all.
  it('hashes on this thread when the workers crash, up to and past the failed-start limit', async () => {
    expect(FAILED_START_LIMIT).toBe(2)
    const workers = installCapturingWorkers<FieldDigestRequest>()
    const { digestFieldsOffThread } = await loadClient()
    const first = [
      encoder.encode('first'),
    ]
    const second = [
      encoder.encode('second'),
    ]

    const firstDigest = digestFieldsOffThread(first)
    const secondDigest = digestFieldsOffThread(second)
    spawnedWorker(workers, 0).onerror?.()

    await expect(firstDigest).resolves.toBe(digestFields(first))
    expect(spawnedWorker(workers, 0).terminate).toHaveBeenCalledOnce()
    // The fresh worker received the request that the crash did not fail.
    expect(onlyMessage(spawnedWorker(workers, 1)).fields.map(field => [...field])).toEqual([
      [...second[0]!],
    ])

    spawnedWorker(workers, 1).onerror?.()

    await expect(secondDigest).resolves.toBe(digestFields(second))
    expect(spawnedWorker(workers, 1).terminate).toHaveBeenCalledOnce()
    expect(workers).toHaveLength(2)
  })

  it('hashes on this thread when the Worker cannot be created', async () => {
    vi.stubGlobal('Worker', class ThrowingWorker {
      constructor() {
        throw new Error('blocked by CSP')
      }
    })
    const { digestFieldsOffThread } = await loadClient()
    const fields = [
      encoder.encode('no worker'),
    ]

    await expect(digestFieldsOffThread(fields)).resolves.toBe(digestFields(fields))
  })

  describe('with a worker that never answers', () => {
    afterEach(() => {
      vi.useRealTimers()
    })

    // A send waits on this digest, and the budget covers the worker's load too: a
    // module fetch that stalls must not hold the send past the budget.
    it('hashes on this thread once the time budget runs out, and terminates the worker', async () => {
      const workers = installCapturingWorkers<FieldDigestRequest>()
      const { digestFieldsOffThread } = await loadClient()
      // The fake clock holds the timers alone, so that the event loop yields of
      // the sliced fallback stay real.
      vi.useFakeTimers({
        toFake: ['setTimeout', 'clearTimeout'],
      })
      const fields = [
        encoder.encode('silent'),
      ]
      const budgetMs = fieldDigestBudgetMs(fields[0]!.byteLength)
      let digest: string | 'pending' = 'pending'
      const done = digestFieldsOffThread(fields).then((value) => {
        digest = value
      })

      await vi.advanceTimersByTimeAsync(budgetMs - 1)
      expect(digest).toBe('pending')
      await vi.advanceTimersByTimeAsync(1)
      await done

      expect(digest).toBe(digestFields(fields))
      expect(spawnedWorker(workers, 0).terminate).toHaveBeenCalledOnce()
    })

    // Two fields of 768 KiB start two MiB together, and one MiB each.
    it('gives the request the budget of the bytes of every field together', async () => {
      const workers = installCapturingWorkers<FieldDigestRequest>()
      const { digestFieldsOffThread } = await loadClient()
      vi.useFakeTimers({
        toFake: ['setTimeout', 'clearTimeout'],
      })
      const fields = [
        new Uint8Array(768 * 1024),
        new Uint8Array(768 * 1024).fill(1),
      ]
      const budgetMs = fieldDigestBudgetMs(2 * 768 * 1024)
      expect(budgetMs).toBe(FIELD_DIGEST_BASE_BUDGET_MS + 2 * FIELD_DIGEST_BUDGET_MS_PER_MIB)
      let digest: string | 'pending' = 'pending'
      const done = digestFieldsOffThread(fields).then((value) => {
        digest = value
      })

      // Past the budget of one field alone, the worker still has time.
      await vi.advanceTimersByTimeAsync(fieldDigestBudgetMs(768 * 1024))
      expect(spawnedWorker(workers, 0).terminate).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(budgetMs - fieldDigestBudgetMs(768 * 1024) - 1)
      expect(digest).toBe('pending')
      await vi.advanceTimersByTimeAsync(1)
      await done

      expect(spawnedWorker(workers, 0).terminate).toHaveBeenCalledOnce()
      expect(digest).toBe(digestFields(fields))
    })
  })
})

describe('fieldDigestBudgetMs', () => {
  it('gives the base budget for no bytes', () => {
    expect(fieldDigestBudgetMs(0)).toBe(FIELD_DIGEST_BASE_BUDGET_MS)
  })

  it('adds the allowance once for each MiB that the bytes start', () => {
    const mib = 1024 * 1024
    expect(fieldDigestBudgetMs(1)).toBe(FIELD_DIGEST_BASE_BUDGET_MS + FIELD_DIGEST_BUDGET_MS_PER_MIB)
    expect(fieldDigestBudgetMs(mib)).toBe(FIELD_DIGEST_BASE_BUDGET_MS + 250)
    expect(fieldDigestBudgetMs(mib + 1)).toBe(FIELD_DIGEST_BASE_BUDGET_MS + 500)
    expect(fieldDigestBudgetMs(64 * mib)).toBe(FIELD_DIGEST_BASE_BUDGET_MS + 16_000)
  })
})
