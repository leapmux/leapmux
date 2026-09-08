import type { Accessor } from 'solid-js'
import { createEffect, on, untrack } from 'solid-js'
import { createGuardedFetch } from '~/hooks/createGuardedFetch'

/** The fetch args every worker-scoped list takes: at minimum, which worker. */
export interface WorkerScopedArgs {
  workerId: string
}

export interface CreateWorkerScopedListOpts<Args extends WorkerScopedArgs, Resp> {
  /**
   * Which worker this list is about, ALWAYS -- never null to mean "not now".
   *
   * The worker is tracked separately from the fetch gate on purpose. A source
   * that answered null for "do not fetch yet" also hid WHICH worker the
   * caller was on, so a caller whose gate happened to close on the same tick
   * the worker changed kept serving the previous worker's answer. Splitting
   * the two makes that impossible: the clear follows the worker, and the gate
   * decides only whether to ask.
   *
   * Empty means "no worker yet"; nothing is fetched and nothing is cleared.
   */
  workerId: Accessor<string>
  /**
   * Whether to fetch NOW. A gate that closes keeps the cached list, so a
   * caller that ties it to "the menu is open" re-opens with no round trip.
   * Defaults to always.
   */
  enabled?: Accessor<boolean>
  /** The rest of the fetch args, read untracked when a fetch starts. */
  args: Accessor<Omit<Args, 'workerId'>>
  fetch: (args: Args, signal: AbortSignal) => Promise<Resp>
  /** Store the answer. Runs only on success. */
  applySuccess: (resp: Resp, args: Args) => void
  /** Drop the previous worker's answer, before the new fetch starts. */
  clear: () => void
  onError?: (err: unknown) => void
}

export interface WorkerScopedList {
  loading: Accessor<boolean>
  /**
   * Re-run the fetch for the current worker. The worker-driven effect fires on
   * a workerId TRANSITION only, so a transient failure on the current worker
   * would otherwise leave the caller with no list and no way back. No-op while
   * there is no worker.
   *
   * Deliberately NOT gated by `enabled`: it is the caller's own explicit
   * retry, and a Refresh button must work whatever the gate says.
   */
  refresh: () => Promise<void>
}

/**
 * The retry-and-clear policy every per-worker list obeys, in one place.
 *
 * `useAvailableShells` and `useAvailableProviders` each held their own copy of
 * this skeleton -- the same success sentinel, the same `on(workerId)` effect
 * with the same two early returns, the same clear-then-`untrack(source)`
 * sequence, and the same `refresh`. Only the payload each stores differed. Two
 * copies of a rule this subtle is one copy too many: the sentinel in particular
 * has a comment in both explaining a past bug, and a fix to one would not have
 * reached the other.
 *
 * Four properties this owns, and no caller restates:
 *
 *   - The sentinel advances ONLY on success, so a failed fetch lets the next
 *     tick with the same workerId retry rather than short-circuit on a stale
 *     value. Stamping it before the fetch locks the caller out of recovering
 *     from a transient failure until the user switches workers and back.
 *   - It tracks the workerId SCALAR, not an args accessor. Caller closures
 *     build a fresh args object every tick, so tracking the accessor re-fires
 *     the effect on identity churn that changes nothing.
 *   - It clears BEFORE the fetch, so the previous worker's answer can never be
 *     offered for the new one during the window the fetch is in flight.
 *   - It clears on EVERY worker change, including one where the gate is shut.
 *     The gate and the worker are separate inputs for exactly this reason: a
 *     caller whose gate closes on the same tick the worker changes -- the
 *     directory picker's drive list does, because the gate is "the worker runs
 *     Windows" -- would otherwise keep offering the previous worker's answer
 *     for the new one, with no transition left to clear it.
 */
export function createWorkerScopedList<Args extends WorkerScopedArgs, Resp>(
  opts: CreateWorkerScopedListOpts<Args, Resp>,
): WorkerScopedList {
  // The worker whose answer is on screen. Advances ONLY on success.
  let lastLoadedWorkerId = ''
  // The worker a fetch is open for. Cleared on both outcomes, so a failure
  // leaves the next tick free to retry the same worker.
  let requestedWorkerId = ''

  const fetcher = createGuardedFetch<Args, Resp>({
    fetch: opts.fetch,
    applySuccess: (resp, args) => {
      opts.applySuccess(resp, args)
      lastLoadedWorkerId = args.workerId
      requestedWorkerId = ''
    },
    onError: (err) => {
      requestedWorkerId = ''
      opts.onError?.(err)
      opts.clear()
    },
  })

  // `GuardedFetchRun<Args>` is a conditional type that picks a no-argument
  // signature when `Args` is `void`. TypeScript cannot resolve it while `Args`
  // is still a type parameter, so it falls back to the intersection of both
  // branches and rejects a plain `Args`. The `extends WorkerScopedArgs` bound
  // already excludes the `void` branch, so this states the arm that applies.
  const run = fetcher.run as (args: Args | null) => Promise<void>

  const argsFor = (workerId: string): Args =>
    ({ ...untrack(opts.args), workerId }) as Args

  // ONE effect over both inputs. Two effects would each see a fetch the other
  // had just started but not yet finished -- the success sentinel advances on
  // success alone -- and would issue it twice.
  createEffect(on(
    [opts.workerId, () => opts.enabled?.() ?? true],
    ([workerId, enabled]) => {
      // BEFORE the gate, and before any decision to fetch. An answer belongs
      // to the worker it came from, so the moment the worker is a different
      // one that answer must go -- whether or not this new worker is one we
      // may ask about, and whether or not it is a worker at all.
      if (lastLoadedWorkerId !== '' && workerId !== lastLoadedWorkerId) {
        opts.clear()
        lastLoadedWorkerId = ''
      }
      if (!workerId || !enabled)
        return
      // Already answered, or already asked. `requestedWorkerId` is what stops
      // the double fetch: the sentinel above cannot, because it waits for the
      // response this run is about to await.
      if (workerId === lastLoadedWorkerId || workerId === requestedWorkerId)
        return
      requestedWorkerId = workerId
      void run(argsFor(workerId))
    },
  ))

  const refresh = async (): Promise<void> => {
    const workerId = untrack(opts.workerId)
    if (!workerId)
      return
    requestedWorkerId = workerId
    // `lastLoadedWorkerId` is deliberately NOT cleared here. The worker-driven
    // effect consults it on a workerId TRANSITION only, so a manual refresh
    // against the current worker just re-fetches and re-stamps it on success --
    // or leaves it untouched on failure, which preserves the retry rule above.
    await run(argsFor(workerId))
  }

  return { loading: fetcher.loading, refresh }
}
