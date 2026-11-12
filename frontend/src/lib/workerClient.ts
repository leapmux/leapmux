// ---------------------------------------------------------------------------
// Shared lazy Web Worker client lifecycle
//
// The Shiki token worker, the markdown render worker, the SLH-DSA verify worker
// and the diff worker each drive their worker the same way: lazily spawn ONE
// module worker on first use, dispatch each request under an incrementing id,
// resolve each reply by id, and -- on a worker crash (`onerror`) or a
// `postMessage` failure -- resolve every pending request to a failure value,
// terminate the dead worker, and drop it so the next request respawns a fresh one.
// This factory owns that lifecycle so the clients cannot drift on its subtle
// parts: the `worker !== failedWorker` stale-crash guard (a crash from an
// already-replaced worker must not wipe the live worker's pending set),
// terminate-before-respawn, and clearing pending on failure.
//
// A request may also carry a time budget (see WorkerRequestOptions): a worker that
// spends longer on it is terminated, and the requests behind it move to a fresh one.
//
// Each client layers its own concerns on top of `request`:
// - the token client a result cache and in-flight coalescing
// - the markdown client its size-based lanes
// - the verify client a fallback to the calling thread
// - the diff client a content cache, in-flight coalescing and a priority gate
// ---------------------------------------------------------------------------

export interface WorkerRequestOptions {
  /**
   * The longest the worker may spend on this request, in milliseconds, counted
   * from the moment the request reaches the front of the worker's queue: a worker
   * answers in order, so a request is never charged for a slow one ahead of it.
   *
   * Past the budget, the client terminates the worker, resolves THIS request to
   * `failureValue`, and posts every other pending request again to a fresh worker,
   * which answers them as the old one would have. A synchronous task cannot be
   * interrupted inside a worker, so terminating the worker is the only way to stop
   * it. Absent: no limit.
   */
  timeBudgetMs?: number
}

export interface WorkerClient<Req, Res> {
  /**
   * Post a request built from a fresh id and resolve with its reply value, or the
   * factory's `failureValue` when the worker can't be created, its `postMessage`
   * throws, it crashes before replying, or it runs past the request's time
   * budget. Never rejects.
   */
  request: (buildMessage: (id: number) => Req, options?: WorkerRequestOptions) => Promise<Res>
  /**
   * Spawn the worker now, ahead of its first request, so that it loads while the
   * caller waits on something else. A worker that cannot spawn is left for the
   * first request, which resolves to the failure value.
   */
  prepare: () => void
}

export interface WorkerClientOptions<Res> {
  /** Spawn a fresh worker. Called lazily on first request; may throw (caught -> failureValue). */
  spawn: () => Worker
  /** Extract the reply id and its resolved value from a worker message's `data`. */
  extract: (data: any) => { id: number, value: Res }
  /** Resolved to every pending request when the worker can't spawn / postMessage fails / it crashes. */
  failureValue: Res
}

/** A posted request that has no reply yet. */
interface PendingRequest<Req, Res> {
  resolve: (value: Res) => void
  /** Kept so the request can be posted again to a fresh worker under its same id. */
  buildMessage: (id: number) => Req
  timeBudgetMs: number | undefined
}

export function createWorkerClient<Req, Res>(opts: WorkerClientOptions<Res>): WorkerClient<Req, Res> {
  let worker: Worker | null = null
  let nextId = 0
  // In post order: a Map iterates in insertion order, and the worker answers in
  // that order, so the first entry is the request the worker works on now.
  const pending = new Map<number, PendingRequest<Req, Res>>()
  let budget: { id: number, timer: ReturnType<typeof setTimeout> } | undefined

  function clearBudget(): void {
    if (budget === undefined)
      return
    clearTimeout(budget.timer)
    budget = undefined
  }

  function failWorker(failedWorker: Worker | null): void {
    failedWorker?.terminate()
    // A stale crash from an already-replaced worker must not resolve the live worker's
    // pending requests -- only the current worker's failure clears the pending set.
    if (worker !== failedWorker)
      return
    clearBudget()
    for (const request of pending.values())
      request.resolve(opts.failureValue)
    pending.clear()
    worker = null
  }

  /** Arm the time budget of the request at the front of the queue, once per request. */
  function armFrontBudget(): void {
    const front = pending.entries().next()
    if (front.done === true) {
      clearBudget()
      return
    }
    const [id, request] = front.value
    if (budget?.id === id)
      return
    clearBudget()
    if (request.timeBudgetMs !== undefined) {
      budget = {
        id,
        timer: setTimeout(exceedBudget, request.timeBudgetMs, id),
      }
    }
  }

  /** The front request ran past its budget: stop the worker and move the rest to a fresh one. */
  function exceedBudget(id: number): void {
    budget = undefined
    const overdue = pending.get(id)
    if (overdue === undefined)
      return
    pending.delete(id)
    const stuckWorker = worker
    worker = null
    stuckWorker?.terminate()
    overdue.resolve(opts.failureValue)
    postPendingAgain()
  }

  /** Post every pending request again, in order and under its same id, to a fresh worker. */
  function postPendingAgain(): void {
    if (pending.size === 0)
      return
    let freshWorker: Worker
    try {
      freshWorker = getWorker()
    }
    catch {
      worker = null
      for (const request of pending.values())
        request.resolve(opts.failureValue)
      pending.clear()
      return
    }
    for (const [id, request] of pending) {
      try {
        freshWorker.postMessage(request.buildMessage(id))
      }
      catch {
        failWorker(freshWorker)
        return
      }
    }
    armFrontBudget()
  }

  function getWorker(): Worker {
    if (!worker) {
      const nextWorker = opts.spawn()
      worker = nextWorker
      nextWorker.onmessage = (e: MessageEvent) => {
        // A reply that a terminated worker queued before it stopped belongs to a
        // request that a fresh worker now answers.
        if (worker !== nextWorker)
          return
        const { id, value } = opts.extract(e.data)
        const request = pending.get(id)
        if (request) {
          pending.delete(id)
          request.resolve(value)
          armFrontBudget()
        }
      }
      // On crash, resolve all pending to the failure value and respawn on next call.
      // Terminate the dead worker first so its thread + its highlighter aren't leaked.
      nextWorker.onerror = () => failWorker(nextWorker)
    }
    return worker
  }

  function request(buildMessage: (id: number) => Req, options?: WorkerRequestOptions): Promise<Res> {
    const id = nextId++
    let w: Worker
    try {
      w = getWorker()
    }
    catch {
      worker = null
      return Promise.resolve(opts.failureValue)
    }
    return new Promise<Res>((resolve) => {
      pending.set(id, { resolve, buildMessage, timeBudgetMs: options?.timeBudgetMs })
      try {
        w.postMessage(buildMessage(id))
      }
      catch {
        failWorker(w)
        return
      }
      armFrontBudget()
    })
  }

  function prepare(): void {
    try {
      getWorker()
    }
    catch {
      worker = null
    }
  }

  return { request, prepare }
}
