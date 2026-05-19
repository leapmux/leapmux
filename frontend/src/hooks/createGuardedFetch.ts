import type { Accessor } from 'solid-js'
import { batch, createSignal, getOwner, onCleanup } from 'solid-js'
import { createLogger } from '~/lib/logger'

const log = createLogger('createGuardedFetch')

/**
 * Generic generation-guarded fetch helper. Owns a `loading` signal and a
 * monotonically-incrementing generation counter so concurrent triggers
 * supersede in-flight runs cleanly: the older run's `applySuccess` /
 * `onError` / final-loading-clear are all skipped when a newer run has
 * started, and `run(null)` cancels any in-flight work and clears
 * loading.
 *
 * Each run also gets a fresh `AbortSignal` that's aborted on supersede
 * or on `run(null)`. The signal is passed to `options.fetch`, so callers
 * whose underlying work supports cancellation (`fetch`, AbortController-
 * aware RPC transports — including `workerRpc.getGitInfo` /
 * `workerRpc.inspectBranchChange` / `workerRpc.inspectBranchDeletion` /
 * `workerRpc.inspectLastTabClose`, which thread the signal through the
 * E2EE channel manager and reject the pending promise on abort) can
 * stop waiting on the response on the way out, not just discard its
 * eventual result. The encrypted channel has no per-call cancellation
 * message today, so the worker's in-flight work still runs to
 * completion — but the client stops holding a pendingRequest entry
 * and the dialog's promise rejects immediately. Callers whose work
 * doesn't support cancellation can ignore the signal — generation
 * guarding alone still drops the stale response.
 *
 * Both `applySuccess` and `onError` run inside a batch with
 * `loading=false`, so callers that touch multiple signals (e.g. a
 * `<select>` whose options + value must update together) get atomic
 * updates without spelling out their own `batch(...)`.
 *
 * `run`'s arg signature: callers with stable args (the fetcher closes
 * over its workerId / path) declare `Args = void` and call `run()`;
 * callers with per-invocation args declare them and call `run({...})`.
 * Both can still cancel with `run(null)`.
 */
export type GuardedFetchRun<Args>
  = [Args] extends [void]
    ? (args?: null) => Promise<void>
    : (args: Args | null) => Promise<void>

export interface GuardedFetch<Args> {
  /**
   * Trigger a fetch (with `args` when the type requires them), or
   * cancel + clear loading with `null`. Aborts any prior in-flight
   * run's signal regardless. Resolves once the fetch settles (success
   * or rejection).
   */
  run: GuardedFetchRun<Args>
  loading: Accessor<boolean>
}

export interface GuardedFetchOptions<Args, T> {
  /**
   * The async work to run. The signal is aborted when the run is
   * superseded by a newer `run(args)` or by `run(null)`. Callers may
   * pass it through to `fetch`/RPC layers to actually cancel work, or
   * ignore it (the helper still drops the stale result either way).
   */
  fetch: (args: Args, signal: AbortSignal) => Promise<T>
  /**
   * Apply the successful response to caller state. Runs inside a batch
   * with `loading` flipped to false, so any setSignal calls here land in
   * the same render pass as the spinner clearing.
   */
  applySuccess: (data: T, args: Args) => void
  /**
   * Optional rejection handler. Runs only for the latest in-flight run.
   * Receives the same `signal` `fetch` got, so callers can distinguish
   * an expected abort (`signal.aborted === true`) from a real failure.
   * The helper itself ignores abort-driven rejections automatically via
   * generation guarding — `onError` is only invoked when the current
   * run rejects with something other than its own abort.
   */
  onError?: (err: unknown, args: Args) => void
  /**
   * Per-run: when this returns true, skip the `setLoading(true)` flash
   * for the next `run` call. Useful when previous data is good enough
   * as a placeholder until the new probe completes (avoids spinner
   * flicker on repo→repo transitions).
   */
  skipLoadingFlash?: () => boolean
}

export function createGuardedFetch<Args, T>(options: GuardedFetchOptions<Args, T>): GuardedFetch<Args> {
  const [loading, setLoading] = createSignal(false)
  let generation = 0
  let inflight: AbortController | null = null
  let disposed = false

  const abortInflight = (reason: string) => {
    if (inflight && !inflight.signal.aborted)
      inflight.abort(new DOMException(reason, 'AbortError'))
    inflight = null
  }

  // Owner-scoped cleanup so a dialog dismissed mid-probe aborts the
  // network round-trip instead of letting it resolve into a dead
  // reactive scope. Bumping the generation also defangs the eventual
  // applySuccess/onError if the abort doesn't reach the transport in
  // time. The `disposed` flag fences runImpl too: a stale closure
  // (e.g. a Promise.then handler captured before dispose) that calls
  // run() after cleanup would otherwise re-arm inflight and write
  // signals on a torn-down reactive scope.
  if (getOwner()) {
    onCleanup(() => {
      disposed = true
      generation++
      abortInflight('owner-disposed')
      setLoading(false)
    })
  }

  // Internal run accepts `Args | null | undefined`; `undefined` only
  // reaches here when Args is void and the caller invoked `run()`.
  // In that case the absent arg is forwarded to fetch as `undefined`,
  // which matches the `void`-arg fetcher's declared signature.
  const runImpl = async (args: Args | null | undefined): Promise<void> => {
    if (disposed)
      return
    const gen = ++generation
    abortInflight('superseded')
    if (args === null) {
      setLoading(false)
      return
    }
    const controller = new AbortController()
    inflight = controller
    if (!options.skipLoadingFlash?.())
      setLoading(true)
    let data: T
    try {
      data = await options.fetch(args as Args, controller.signal)
    }
    catch (err) {
      // disposed-check is redundant with the generation check (the
      // cleanup bumps generation), but keeping it explicit lets a
      // future reader see at a glance that no post-dispose state is
      // touched here.
      if (disposed || gen !== generation)
        return
      inflight = null
      // Mirror the success path: clear loading and apply caller state
      // in one batch, so a single render pass observes both. Without
      // this, an onError that touches multiple signals fires two
      // notifications (one per signal, plus the loading clear).
      batch(() => {
        setLoading(false)
        options.onError?.(err, args as Args)
      })
      return
    }
    if (disposed || gen !== generation)
      return
    inflight = null
    // applySuccess deliberately runs in its own try/catch — distinct
    // from the fetch error path above. A throw here is a programming
    // bug in the caller (proto field misuse, a bad setSignal listener),
    // not a fetch failure, so it must NOT route to onError or partially
    // apply state alongside an "error" UX. Log it instead so the bug
    // stays visible without unmounting the dialog through an unhandled
    // rejection.
    batch(() => {
      setLoading(false)
      try {
        options.applySuccess(data, args as Args)
      }
      catch (applyErr) {
        log.warn('applySuccess threw', applyErr)
      }
    })
  }

  return { run: runImpl as GuardedFetchRun<Args>, loading }
}
