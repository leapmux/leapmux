import type { Accessor } from 'solid-js'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createSignal } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { createWorkerScopedList } from '~/hooks/createWorkerScopedList'

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
 * that asks about ONE stated worker on demand.
 *
 * The sibling of {@link import('./useAvailableShells').useAvailableShells}, and
 * deliberately the same shape: `source` returns the fetch args or `null` to
 * skip, the hook fetches on the first non-null value and re-fetches only when
 * the workerId changes, and a null source keeps the cached list rather than
 * clearing it — so a caller that ties the source to "the menu is open"
 * re-opens without a second round trip.
 *
 * `useAgentOperations.loadAvailableProviders` is NOT this hook and does not use
 * it. That one scans for whichever worker the ACTIVE TAB is on, and it carries
 * its own abort-and-supersede rules plus a keep-the-previous-list-on-failure
 * policy for that moving target. This hook answers for a worker the caller
 * states, which is what a branch row needs: the branch's own machine, not the
 * machine the current tab happens to sit on.
 */
export function useAvailableProviders(
  source: Accessor<UseAvailableProvidersArgs | null>,
  onError?: (err: unknown) => void,
): UseAvailableProvidersResult {
  const [providers, setProviders] = createSignal<AgentProvider[] | undefined>(undefined)

  // Every rule about WHEN to fetch, when to retry and when to clear lives in
  // `createWorkerScopedList`. This hook keeps only the payload it stores.
  const list = createWorkerScopedList<UseAvailableProvidersArgs, Awaited<ReturnType<typeof workerRpc.listAvailableProviders>>>({
    source,
    fetch: (args, signal) => workerRpc.listAvailableProviders(args.workerId, { signal }),
    applySuccess: resp => setProviders([...resp.providers]),
    // The previous worker's list is not an answer for this one, and
    // `undefined` is the "not asked yet" state a caller distinguishes from `[]`.
    clear: () => setProviders(undefined),
    onError,
  })

  return { providers, loading: list.loading, refresh: list.refresh }
}
