// ---------------------------------------------------------------------------
// Shared lazy Web Worker client lifecycle
//
// Both the Shiki token worker and the markdown render worker drive their worker
// the same way: lazily spawn ONE module worker on first use, dispatch each request
// under an incrementing id, resolve each reply by id, and -- on a worker crash
// (`onerror`) or a `postMessage` failure -- resolve every pending request to a
// failure value, terminate the dead worker, and drop it so the next request
// respawns a fresh one. This factory owns that lifecycle so the two clients can't
// drift on its subtle parts: the `worker !== failedWorker` stale-crash guard (a
// crash from an already-replaced worker must not wipe the live worker's pending
// set), terminate-before-respawn, and clearing pending on failure.
//
// Each client layers its own concerns on top of `request` -- the token client a
// result cache + in-flight coalescing; the markdown client nothing extra.
// ---------------------------------------------------------------------------

export interface WorkerClient<Req, Res> {
  /**
   * Post a request built from a fresh id and resolve with its reply value, or the
   * factory's `failureValue` when the worker can't be created, its `postMessage`
   * throws, or it crashes before replying. Never rejects.
   */
  request: (buildMessage: (id: number) => Req) => Promise<Res>
}

export interface WorkerClientOptions<Res> {
  /** Spawn a fresh worker. Called lazily on first request; may throw (caught -> failureValue). */
  spawn: () => Worker
  /** Extract the reply id and its resolved value from a worker message's `data`. */
  extract: (data: any) => { id: number, value: Res }
  /** Resolved to every pending request when the worker can't spawn / postMessage fails / it crashes. */
  failureValue: Res
}

export function createWorkerClient<Req, Res>(opts: WorkerClientOptions<Res>): WorkerClient<Req, Res> {
  let worker: Worker | null = null
  let nextId = 0
  const pending = new Map<number, (value: Res) => void>()

  function failWorker(failedWorker: Worker | null): void {
    failedWorker?.terminate()
    // A stale crash from an already-replaced worker must not resolve the live worker's
    // pending requests -- only the current worker's failure clears the pending set.
    if (worker !== failedWorker)
      return
    for (const resolve of pending.values())
      resolve(opts.failureValue)
    pending.clear()
    worker = null
  }

  function getWorker(): Worker {
    if (!worker) {
      const nextWorker = opts.spawn()
      worker = nextWorker
      nextWorker.onmessage = (e: MessageEvent) => {
        const { id, value } = opts.extract(e.data)
        const resolve = pending.get(id)
        if (resolve) {
          pending.delete(id)
          resolve(value)
        }
      }
      // On crash, resolve all pending to the failure value and respawn on next call.
      // Terminate the dead worker first so its thread + its highlighter aren't leaked.
      nextWorker.onerror = () => failWorker(nextWorker)
    }
    return worker
  }

  function request(buildMessage: (id: number) => Req): Promise<Res> {
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
      pending.set(id, resolve)
      try {
        w.postMessage(buildMessage(id))
      }
      catch {
        failWorker(w)
      }
    })
  }

  return { request }
}
