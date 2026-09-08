import type { Accessor } from 'solid-js'
import type { WorkerScopedArgs } from '~/hooks/createWorkerScopedList'
import { createSignal } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { createWorkerScopedList } from '~/hooks/createWorkerScopedList'

interface UseFilesystemRootsResult {
  /** The roots the worker reported, or `[]` before one answers. */
  roots: Accessor<string[]>
  loading: Accessor<boolean>
  /**
   * Manual retry for the current worker. The worker-change effect fires on a
   * workerId transition alone, so a transient failure would otherwise leave
   * the caller with no list until the user picked another worker.
   */
  refresh: () => Promise<void>
}

/**
 * Reactive wrapper around the `listFilesystemRoots` worker RPC.
 *
 * Plain signals plus an effect rather than `createResource`, for the reason
 * `useAvailableShells` states: the router's Suspense boundary unmounts the
 * whole route while a resource loads, flashing blank under any dialog that
 * reads it during the first fetch.
 *
 * `workerId` says WHICH worker; `enabled` says whether to ask. A POSIX caller
 * passes `enabled: false`: `filesystemRoot` already knows that a POSIX worker
 * has exactly one root, so the round trip would buy nothing. The two are
 * separate so that a switch to a POSIX worker still CLEARS the previous
 * worker's drives -- see {@link createWorkerScopedList}.
 *
 * Every rule about when to fetch, when to retry and when to clear lives in
 * that helper. This hook keeps only the list it stores.
 */
export function useFilesystemRoots(
  workerId: Accessor<string>,
  enabled: Accessor<boolean>,
  onError?: (err: unknown) => void,
): UseFilesystemRootsResult {
  const [roots, setRoots] = createSignal<string[]>([])

  const list = createWorkerScopedList<WorkerScopedArgs, Awaited<ReturnType<typeof workerRpc.listFilesystemRoots>>>({
    workerId,
    enabled,
    args: () => ({}),
    fetch: args => workerRpc.listFilesystemRoots(args.workerId),
    applySuccess: resp => setRoots(resp.roots),
    // Cleared with the worker, so one worker's drives are never offered for
    // another. A caller that shows a drive menu hides it again in that window,
    // which is correct: it does not yet know what the new worker has.
    clear: () => setRoots([]),
    onError,
  })

  return { roots, loading: list.loading, refresh: list.refresh }
}
