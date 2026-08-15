import { concatBytes } from './bytes'
import { createLogger } from './logger'

const log = createLogger('inputQueue')

/**
 * Shared per-key batch queues, drained one send at a time.
 *
 * The worker dispatches every inner RPC on its own goroutine, so two sends in
 * flight together can arrive transposed. Keeping a single send in flight per
 * key removes the race at the only point that can see the true order, which
 * is the sender. The entry's presence in the map IS the drain lock: an
 * enqueue that finds an entry joins the running drain; only an enqueue on a
 * missing entry may start one. Create the queues ONCE (module scope of the
 * consumer) — a second instance would race a second send for the same key.
 *
 * A joining enqueue REPLACES the entry's drain, so the most recent owner
 * decides every next batch. Owners that remount mid-burst therefore take
 * their own terminals back instead of staying served by the previous owner's
 * closures.
 */
export interface InputQueueDrain<K, C> {
  /** Resolve the target before each batch. Return undefined to drop the queue. */
  resolve: (key: K) => C | undefined
  /** Send one batch. A rejection retries the batch once, then drops it. */
  send: (key: K, ctx: C, batch: Uint8Array) => Promise<void>
  /**
   * How long one send may stay in flight before the drain moves on without
   * it. The deadline covers the WHOLE send — every leg it awaits — unlike a
   * per-RPC timeout that arms only once the transport is open. Size it past
   * the receiver's own response deadline: a drain that moves on while its
   * send may still deliver re-opens the transposition race the one-in-flight
   * rule exists to close.
   */
  sendDeadlineMs: () => number
}

export interface SharedInputQueues<K, C> {
  /**
   * Queue `data` for `key`. Resolves when the drain that took `data`
   * finishes — for every enqueue of the burst, the one that started the
   * drain included. Resolution does not promise delivery: a batch the drain
   * dropped (target gone) or lost (send failed twice, deadline passed) is
   * gone by then.
   */
  enqueue: (key: K, data: Uint8Array, drain: InputQueueDrain<K, C>) => Promise<void>
}

interface QueueEntry<K, C> {
  batches: Uint8Array[]
  drain: InputQueueDrain<K, C>
  /** Settled when the entry's drain ends, for every enqueue it took. */
  waiters: Array<() => void>
}

type SendOutcome = 'sent' | 'rejected' | 'deadline'

/**
 * Run one send under the drain's deadline. The send's own rejection is
 * reported as `rejected` — distinct from `deadline`, because only a
 * rejection is known not to have run: a send abandoned at its deadline may
 * still deliver later, and its batch must not be re-sent.
 */
async function attemptSend<K, C>(
  drain: InputQueueDrain<K, C>,
  key: K,
  ctx: C,
  batch: Uint8Array,
): Promise<SendOutcome> {
  let settled = false
  return new Promise<SendOutcome>((resolve) => {
    let send: Promise<void>
    try {
      send = drain.send(key, ctx, batch)
    }
    catch {
      // A send that throws before returning a promise ran no I/O; treat it
      // like a rejection so the batch gets its one retry.
      resolve('rejected')
      return
    }
    const timer = setTimeout(() => {
      settled = true
      resolve('deadline')
    }, drain.sendDeadlineMs())
    send.then(
      () => {
        if (!settled)
          resolve('sent')
        clearTimeout(timer)
      },
      () => {
        if (!settled)
          resolve('rejected')
        clearTimeout(timer)
      },
    )
  })
}

export function createSharedInputQueues<K, C>(): SharedInputQueues<K, C> {
  const queues = new Map<K, QueueEntry<K, C>>()

  const drainKey = async (key: K, entry: QueueEntry<K, C>) => {
    const { batches } = entry
    try {
      let canRetry = true
      while (batches.length > 0) {
        const batch = concatBytes(...batches)
        batches.length = 0
        let ctx: C | undefined
        try {
          ctx = entry.drain.resolve(key)
        }
        catch (err) {
          // A resolve that throws is the owner's bug. Treat it as "target
          // gone" and drop the queue — a rejection here would escape into
          // the fire-and-forget input path, which has no error sink.
          log.error('resolve threw; dropping queue', { key, err })
          return
        }
        if (ctx === undefined)
          return
        const outcome = await attemptSend(entry.drain, key, ctx, batch)
        if (outcome === 'sent') {
          canRetry = true
          continue
        }
        if (outcome === 'rejected' && canRetry) {
          // One clean retry per batch: a rejection means the send never ran
          // (channel refused, worker answered with an error), so the same
          // bytes are safe to send again, ahead of anything typed since. A
          // deadline, by contrast, leaves delivery unknown — that batch is
          // dropped, never re-sent.
          canRetry = false
          batches.unshift(batch)
          continue
        }
        // Lost batch. Keep draining: dropping the rest of the queue because
        // one write failed is the worse outcome.
      }
    }
    finally {
      // Safe to drop: the loop only exits with the queue empty or the target
      // gone, and no await separates the check from here.
      queues.delete(key)
      for (const w of entry.waiters)
        w()
    }
  }

  return {
    enqueue(key, data, drain) {
      const existing = queues.get(key)
      if (existing) {
        existing.batches.push(data)
        existing.drain = drain
        return new Promise<void>(resolve => existing.waiters.push(resolve))
      }
      const entry: QueueEntry<K, C> = { batches: [data], drain, waiters: [] }
      queues.set(key, entry)
      return drainKey(key, entry)
    },
  }
}
