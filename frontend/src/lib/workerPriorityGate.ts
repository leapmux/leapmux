/**
 * Two-tier priority gate in front of a Web Worker client.
 *
 * A worker processes its message queue strictly FIFO, so once a burst of
 * requests is posted, their completion order is fixed — a mount burst that
 * dispatches every overscan row's highlight ahead of a viewport row's makes
 * the visible upgrade wait for all of them. This gate keeps at most
 * `maxInFlight` requests posted to the worker and holds the rest client-side,
 * where priority CAN still be decided: each dequeue picks the first job whose
 * `isLow()` currently returns false, falling back to FIFO among low jobs.
 *
 * Priority is a thunk, re-evaluated at dequeue time, so a job enqueued as
 * low-priority (an overscan row) upgrades automatically when its row scrolls
 * into the viewport — no requeue plumbing at the call sites.
 *
 * `maxInFlight` defaults to 2: one request processing on the worker thread
 * plus one queued behind it, so the worker never idles waiting for the next
 * post (round-trip pipelining) while the client keeps reordering power over
 * everything else.
 */
export interface WorkerPriorityGate {
  /**
   * Run `work` when a slot frees, preferring jobs that are currently
   * high-priority. Settles with `work`'s result; a rejection passes through.
   */
  enqueue: <T>(work: () => Promise<T>, isLow?: () => boolean) => Promise<T>
  /** Test/diagnostic: jobs currently held client-side (not yet posted). */
  queuedCount: () => number
}

interface GateJob {
  start: () => void
  isLow: (() => boolean) | undefined
}

export function createWorkerPriorityGate(maxInFlight = 2): WorkerPriorityGate {
  const queue: GateJob[] = []
  let inFlight = 0

  const pickNextIndex = (): number => {
    for (let i = 0; i < queue.length; i++) {
      if (!(queue[i].isLow?.() ?? false))
        return i
    }
    return 0
  }

  const pump = (): void => {
    while (inFlight < maxInFlight && queue.length > 0) {
      const [job] = queue.splice(pickNextIndex(), 1)
      inFlight += 1
      job.start()
    }
  }

  const enqueue = <T>(work: () => Promise<T>, isLow?: () => boolean): Promise<T> => {
    return new Promise<T>((resolve, reject) => {
      queue.push({
        isLow,
        start: () => {
          const settle = (): void => {
            inFlight -= 1
            pump()
          }
          // `work` may throw synchronously (a failed worker spawn) — route it
          // through the same rejection path so the slot is always released.
          let result: Promise<T>
          try {
            result = work()
          }
          catch (err) {
            settle()
            reject(err)
            return
          }
          result.then(
            (value) => {
              settle()
              resolve(value)
            },
            (err) => {
              settle()
              reject(err)
            },
          )
        },
      })
      pump()
    })
  }

  return { enqueue, queuedCount: () => queue.length }
}
