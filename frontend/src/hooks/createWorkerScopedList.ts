import type { Accessor } from 'solid-js'
import { createEffect, on, untrack } from 'solid-js'
import { createGuardedFetch } from '~/hooks/createGuardedFetch'

/** The fetch args every worker-scoped list takes: at minimum, which worker. */
export interface WorkerScopedArgs {
  workerId: string
}

export interface CreateWorkerScopedListOpts<Args extends WorkerScopedArgs, Resp> {
  /** The fetch args, or null to skip. A null source keeps the cached list. */
  source: Accessor<Args | null>
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
   * Re-run the fetch for the current source. The source-driven effect fires on
   * a workerId TRANSITION only, so a transient failure on the current worker
   * would otherwise leave the caller with no list and no way back. No-op while
   * the source returns null.
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
 * Three properties this owns, and no caller restates:
 *
 *   - The sentinel advances ONLY on success, so a failed fetch lets the next
 *     tick with the same workerId retry rather than short-circuit on a stale
 *     value. Stamping it before the fetch locks the caller out of recovering
 *     from a transient failure until the user switches workers and back.
 *   - It tracks the workerId SCALAR, not the source accessor. Caller closures
 *     build a fresh args object every tick, so tracking the accessor re-fires
 *     the effect on identity churn that changes nothing.
 *   - It clears BEFORE the fetch, so the previous worker's answer can never be
 *     offered for the new one during the window the fetch is in flight.
 */
export function createWorkerScopedList<Args extends WorkerScopedArgs, Resp>(
  opts: CreateWorkerScopedListOpts<Args, Resp>,
): WorkerScopedList {
  let lastLoadedWorkerId = ''

  const fetcher = createGuardedFetch<Args, Resp>({
    fetch: opts.fetch,
    applySuccess: (resp, args) => {
      opts.applySuccess(resp, args)
      lastLoadedWorkerId = args.workerId
    },
    onError: (err) => {
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

  const workerIdFromSource = (): string | null => opts.source()?.workerId ?? null
  createEffect(on(workerIdFromSource, (workerId) => {
    if (!workerId)
      return
    if (workerId === lastLoadedWorkerId)
      return
    opts.clear()
    // The `on()` already subscribes through `workerIdFromSource`; read the rest
    // of the args untracked.
    const args = untrack<Args | null>(opts.source)
    if (args === null)
      return
    void run(args)
  }))

  const refresh = async (): Promise<void> => {
    const args = untrack<Args | null>(opts.source)
    if (args === null)
      return
    // `lastLoadedWorkerId` is deliberately NOT cleared here. The source-driven
    // effect consults it on a workerId TRANSITION only, so a manual refresh
    // against the current worker just re-fetches and re-stamps it on success --
    // or leaves it untouched on failure, which preserves the retry rule above.
    await run(args)
  }

  return { loading: fetcher.loading, refresh }
}
