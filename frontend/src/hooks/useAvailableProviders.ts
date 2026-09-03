import type { Accessor } from 'solid-js'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createEffect, createSignal, on, untrack } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { createGuardedFetch } from '~/hooks/createGuardedFetch'

export interface UseAvailableProvidersArgs {
  workerId: string
}

export interface UseAvailableProvidersResult {
  /**
   * The providers the worker reports, or `undefined` before the first
   * successful load. The two states differ: `undefined` means "not asked yet",
   * and `[]` means "this worker has none configured". A menu renders no
   * provider row for either, but a dialog tells the user which one it is.
   */
  providers: Accessor<AgentProvider[] | undefined>
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
 * Reactive wrapper around the ListAvailableProviders worker RPC, for a surface
 * that asks about ONE named worker on demand.
 *
 * The sibling of {@link import('./useAvailableShells').useAvailableShells}, and
 * deliberately the same shape: `source` returns the fetch args or `null` to
 * skip, the hook fetches on the first non-null value and re-fetches only when
 * the workerId changes, and a null source keeps the cached list rather than
 * clearing it — so a caller that gates the source on "the menu is open"
 * re-opens without a second round trip.
 *
 * `useAgentOperations.loadAvailableProviders` is NOT this hook and does not use
 * it. That one scans for whichever worker the ACTIVE TAB is on, and it carries
 * its own abort-and-supersede rules plus a keep-the-previous-list-on-failure
 * policy for that moving target. This hook answers for a worker the caller
 * names, which is what a branch row needs: the branch's own machine, not the
 * machine the current tab happens to sit on.
 */
export function useAvailableProviders(
  source: Accessor<UseAvailableProvidersArgs | null>,
  onError?: (err: unknown) => void,
): UseAvailableProvidersResult {
  const [providers, setProviders] = createSignal<AgentProvider[] | undefined>(undefined)

  // Advances only on a SUCCESSFUL load, so a failed fetch lets the next
  // reactive tick with the same workerId retry instead of short-circuiting on
  // a stale sentinel. Same rule as useAvailableShells, for the same reason.
  let lastLoadedWorkerId = ''

  const fetcher = createGuardedFetch<UseAvailableProvidersArgs, Awaited<ReturnType<typeof workerRpc.listAvailableProviders>>>({
    fetch: (args, signal) => workerRpc.listAvailableProviders(args.workerId, { signal }),
    applySuccess: (resp, args) => {
      setProviders([...resp.providers])
      lastLoadedWorkerId = args.workerId
    },
    onError: (err) => {
      onError?.(err)
      setProviders(undefined)
    },
  })

  // Track the workerId scalar, not the source accessor: caller closures build a
  // fresh args object every tick, and only the workerId gates the fetch.
  const workerIdFromSource = (): string | null => source()?.workerId ?? null
  createEffect(on(workerIdFromSource, (workerId) => {
    if (!workerId)
      return
    if (workerId === lastLoadedWorkerId)
      return
    // The previous worker's list is not an answer for this one. Clear it before
    // the fetch so a caller cannot offer a provider the new worker may not have.
    setProviders(undefined)
    // The `on()` already subscribes through `workerIdFromSource`; read the rest
    // of the args untracked.
    const args = untrack(source)
    if (args === null)
      return
    void fetcher.run(args)
  }))

  const refresh = async (): Promise<void> => {
    const args = untrack(source)
    if (args === null)
      return
    await fetcher.run(args)
  }

  return { providers, loading: fetcher.loading, refresh }
}
