// ---------------------------------------------------------------------------
// Shared lazy Web Worker client lifecycle
//
// A client lazily spawns ONE module worker on first use, posts each request
// under an incrementing id, and resolves each reply by id. This factory owns
// that lifecycle, so that no client repeats its subtle parts:
// - A failure of a worker that the client already replaced never touches the
//   requests of the live worker.
// - The client terminates a failed worker before it spawns the next one.
// - A worker failure fails the request in progress, and the other pending
//   requests move to a fresh worker (see below).
//
// THE WORKER MUST ANSWER IN POST ORDER, one request at a time, whenever a
// request takes a time budget. The oldest request without a reply, the FRONT
// request, is then the one that the worker works on:
// - A time budget runs only while its request is at the front.
// - A crash fails the front request.
// A worker whose handler awaits can work on several requests at once and answer
// out of order. Such a worker must take no time budget, and a crash then fails
// the oldest request, which may not be the one that the worker was working on.
//
// When the worker crashes (`onerror`) or runs past the time budget of the front
// request (see WorkerRequestOptions), the client:
// 1. Terminates the worker.
// 2. Resolves the front request to the failure value.
// 3. Posts every other pending request again, in order and under its same id,
//    to a fresh worker, which answers it as the failed worker would have.
// A worker that crashes before it answers anything most likely cannot load its
// script. After FAILED_START_LIMIT such crashes in a row, a crash resolves every
// pending request to the failure value instead, so that a worker that cannot
// load does not spawn once for each pending request.
//
// A request whose message cannot be built or posted fails alone. `postMessage`
// throws only when it cannot clone the message, before the message reaches the
// worker, so the worker and its other requests are unaffected.
// ---------------------------------------------------------------------------

/**
 * The default cold-start allowance (see WorkerClientOptions.coldStartAllowanceMs).
 * The largest worker, the Markdown worker, loads about 1.1 MB of compressed
 * script on its first start, which takes about 20 s over a 500 kbit/s link.
 */
export const WORKER_COLD_START_ALLOWANCE_MS = 30_000

/**
 * The number of workers in a row that may crash before they answer anything.
 * The crash that reaches it, and each later one before a reply, resolves every
 * pending request to the failure value instead of posting the requests again.
 * A reply from a worker sets the count back to zero. A run past a time budget
 * never counts: it is a slow request, not a worker that cannot load.
 */
export const FAILED_START_LIMIT = 2

export interface WorkerRequestOptions {
  /**
   * The longest the worker may spend on this request, in milliseconds, counted
   * from the moment the request reaches the front of the worker's queue: a worker
   * answers in order, so a request is never charged for a slow one ahead of it.
   * A worker that has not answered anything yet gets the client's cold-start
   * allowance more (see WorkerClientOptions.coldStartAllowanceMs).
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
   * Post a request built from a fresh id and resolve with its reply value, or
   * with the factory's `failureValue` when the request fails:
   * - No worker can be created.
   * - The message cannot be built or posted.
   * - The worker crashes, or runs past this request's time budget, while it
   *   works on this request.
   * - Workers keep crashing before they answer anything (see FAILED_START_LIMIT).
   * Never rejects.
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
  /** The value that a failed request resolves to (see WorkerClient.request). */
  failureValue: Res
  /**
   * The time that a worker which has not answered anything yet gets in addition
   * to the time budget of its front request, because it may still be loading its
   * script. Default: WORKER_COLD_START_ALLOWANCE_MS. Pass 0 when the budget caps
   * how long the caller waits, so that the budget must cover the load too.
   */
  coldStartAllowanceMs?: number
}

/** A posted request that has no reply yet. */
interface PendingRequest<Req, Res> {
  resolve: (value: Res) => void
  /** Kept so the request can be posted again to a fresh worker under its same id. */
  buildMessage: (id: number) => Req
  timeBudgetMs: number | undefined
}

/** The live worker, and whether it has answered anything: until it does, it may still be loading. */
interface LiveWorker {
  worker: Worker
  answered: boolean
}

export function createWorkerClient<Req, Res>(opts: WorkerClientOptions<Res>): WorkerClient<Req, Res> {
  const coldStartAllowanceMs = opts.coldStartAllowanceMs ?? WORKER_COLD_START_ALLOWANCE_MS
  let live: LiveWorker | null = null
  let nextId = 0
  // In post order: a Map iterates in insertion order, and the worker answers in
  // that order, so the first entry is the request the worker works on now.
  const pending = new Map<number, PendingRequest<Req, Res>>()
  let budget: { id: number, timer: ReturnType<typeof setTimeout> } | undefined
  /** Workers in a row that crashed before they answered anything. */
  let failedStarts = 0

  function clearBudget(): void {
    if (budget === undefined)
      return
    clearTimeout(budget.timer)
    budget = undefined
  }

  function failAllPending(): void {
    clearBudget()
    for (const request of pending.values())
      request.resolve(opts.failureValue)
    pending.clear()
  }

  /**
   * Terminate `failed`, and return whether it was the live worker, which it then
   * no longer is. A failure of an already-replaced worker must not touch the
   * live worker's requests: they were posted to the live worker, not to this one.
   */
  function retire(failed: LiveWorker): boolean {
    failed.worker.terminate()
    if (live !== failed)
      return false
    live = null
    clearBudget()
    return true
  }

  /** The retired worker failed on the front request: fail it, and move the others to a fresh worker. */
  function failFront(): void {
    const front = pending.entries().next()
    if (front.done !== true) {
      const [id, request] = front.value
      pending.delete(id)
      request.resolve(opts.failureValue)
    }
    postPendingAgain()
  }

  /** `stuck` ran past the time budget of its front request. */
  function exceedBudget(stuck: LiveWorker): void {
    if (retire(stuck))
      failFront()
  }

  /** `crashed` fired `error`. */
  function crash(crashed: LiveWorker): void {
    const answered = crashed.answered
    if (!retire(crashed))
      return
    if (!answered)
      failedStarts++
    if (failedStarts >= FAILED_START_LIMIT)
      failAllPending()
    else
      failFront()
  }

  /** Arm the time budget of the request at the front of the queue on `target`, once per request. */
  function armFrontBudget(target: LiveWorker): void {
    const front = pending.entries().next()
    if (front.done === true) {
      clearBudget()
      return
    }
    const [id, request] = front.value
    if (budget?.id === id)
      return
    clearBudget()
    if (request.timeBudgetMs === undefined)
      return
    const allowance = target.answered
      ? 0
      : coldStartAllowanceMs
    budget = {
      id,
      timer: setTimeout(exceedBudget, request.timeBudgetMs + allowance, target),
    }
  }

  /** Post `request` to `target`; a request whose message cannot be built or posted fails alone. */
  function post(target: LiveWorker, id: number, request: PendingRequest<Req, Res>): void {
    try {
      target.worker.postMessage(request.buildMessage(id))
    }
    catch {
      pending.delete(id)
      request.resolve(opts.failureValue)
    }
  }

  /** Post every pending request again, in order and under its same id, to a fresh worker. */
  function postPendingAgain(): void {
    if (pending.size === 0)
      return
    const fresh = liveWorker()
    if (fresh === undefined) {
      failAllPending()
      return
    }
    // Deleting the entry of a failed post while iterating is safe: a Map
    // iterator skips a deleted entry and visits every later one.
    for (const [id, request] of pending)
      post(fresh, id, request)
    armFrontBudget(fresh)
  }

  /** The live worker, spawned now when there is none; undefined when the spawn throws. */
  function liveWorker(): LiveWorker | undefined {
    if (live !== null)
      return live
    let worker: Worker
    try {
      worker = opts.spawn()
    }
    catch {
      return undefined
    }
    const spawned: LiveWorker = { worker, answered: false }
    live = spawned
    worker.onmessage = (e: MessageEvent) => {
      // A reply that a terminated worker queued before it stopped belongs to a
      // request that a fresh worker now answers.
      if (live !== spawned)
        return
      spawned.answered = true
      failedStarts = 0
      const { id, value } = opts.extract(e.data)
      const request = pending.get(id)
      if (request) {
        pending.delete(id)
        request.resolve(value)
        armFrontBudget(spawned)
      }
    }
    // Terminate the dead worker before the next one spawns, so that its thread
    // and its state are not leaked.
    worker.onerror = () => crash(spawned)
    return spawned
  }

  function request(buildMessage: (id: number) => Req, options?: WorkerRequestOptions): Promise<Res> {
    const target = liveWorker()
    if (target === undefined)
      return Promise.resolve(opts.failureValue)
    const id = nextId++
    return new Promise<Res>((resolve) => {
      const entry: PendingRequest<Req, Res> = { resolve, buildMessage, timeBudgetMs: options?.timeBudgetMs }
      pending.set(id, entry)
      post(target, id, entry)
      armFrontBudget(target)
    })
  }

  function prepare(): void {
    liveWorker()
  }

  return { request, prepare }
}
