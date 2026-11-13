/**
 * Three-tier priority gate in front of a Web Worker client.
 *
 * A worker processes its message queue strictly FIFO, so once a burst of
 * requests is posted, their completion order is fixed — a mount burst that
 * dispatches every overscan row's highlight ahead of a viewport row's makes
 * the visible upgrade wait for all of them. This gate keeps at most
 * `maxInFlight` requests posted to the worker and holds the rest client-side,
 * where priority CAN still be decided: each dequeue picks the first `'first'`
 * job, else the first job that is not low, else the first low job (see
 * `WorkerJobPriority`).
 *
 * A low priority is a thunk, re-evaluated at dequeue time, so a job enqueued as
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
   * Run `work` when a slot frees, in the order that `priority` gives (see
   * `WorkerJobPriority`). Settles with `work`'s result; a rejection passes
   * through.
   */
  enqueue: <T>(work: () => Promise<T>, priority?: WorkerJobPriority) => Promise<T>
  /** Test/diagnostic: jobs currently held client-side (not yet posted). */
  queuedCount: () => number
}

/**
 * The place of a job in the queue of the gate. The gate posts the jobs that
 * wait in three tiers, each in arrival order:
 *
 *   1. `'first'`: a result that the user waits for while the user types, such
 *      as the colours of a code block in the composer. A job of the chat that
 *      arrived earlier must not delay it.
 *   2. No priority, or a thunk that returns false: a high-priority job.
 *   3. A thunk that returns true: a low-priority job. The gate calls the thunk
 *      each time it picks a job, so a job can move between tiers 2 and 3.
 *
 * A job that the gate already posted keeps its slot, because the gate cannot
 * take a message back from the worker.
 */
export type WorkerJobPriority = 'first' | (() => boolean)

interface GateJob {
  start: () => void
  priority: WorkerJobPriority | undefined
}

export function createWorkerPriorityGate(maxInFlight = 2): WorkerPriorityGate {
  const queue: GateJob[] = []
  let inFlight = 0

  // A priority thunk that THROWS (e.g. it reads a torn-down reactive owner / disposed
  // virtualizer) must not wedge the gate: this runs inside pump()'s synchronous dispatch
  // loop, so an uncaught throw would abandon the loop with a slot half-claimed and
  // `inFlight` never released, stalling every future job on this client. Treat a throwing
  // (or absent) thunk as high priority so the job dispatches and its slot is freed via the
  // work() path.
  const isLowSafe = (job: GateJob): boolean => {
    if (typeof job.priority !== 'function')
      return false
    try {
      return job.priority()
    }
    catch {
      return false
    }
  }

  const pickNextIndex = (): number => {
    const first = queue.findIndex(job => job.priority === 'first')
    if (first >= 0)
      return first
    for (let i = 0; i < queue.length; i++) {
      const job = queue[i]
      if (job === undefined || !isLowSafe(job))
        return i
    }
    return 0
  }

  const pump = (): void => {
    while (inFlight < maxInFlight && queue.length > 0) {
      const [job] = queue.splice(pickNextIndex(), 1)
      if (job === undefined)
        break
      inFlight += 1
      job.start()
    }
  }

  const enqueue = <T>(work: () => Promise<T>, priority?: WorkerJobPriority): Promise<T> => {
    return new Promise<T>((resolve, reject) => {
      queue.push({
        priority,
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

  return {
    enqueue,
    queuedCount: () => queue.length,
  }
}
