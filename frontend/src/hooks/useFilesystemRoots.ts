import type { Accessor } from 'solid-js'
import { createSignal } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { createWorkerScopedList } from '~/hooks/createWorkerScopedList'

export interface UseFilesystemRootsArgs {
  workerId: string
}

interface UseFilesystemRootsResult {
  /** The roots the worker reported, or `[]` before one answers. */
  roots: Accessor<string[]>
  loading: Accessor<boolean>
  /**
   * Manual retry for the current source. The worker-change effect fires on a
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
 * `source` returns the fetch args, or `null` to skip. A POSIX caller passes
 * null: `filesystemRoot` already knows that a POSIX worker has exactly one
 * root, so the round trip would buy nothing.
 *
 * Every rule about when to fetch, when to retry and when to clear lives in
 * {@link createWorkerScopedList}. This hook keeps only the list it stores.
 */
export function useFilesystemRoots(
  source: Accessor<UseFilesystemRootsArgs | null>,
  onError?: (err: unknown) => void,
): UseFilesystemRootsResult {
  const [roots, setRoots] = createSignal<string[]>([])

  const list = createWorkerScopedList<UseFilesystemRootsArgs, Awaited<ReturnType<typeof workerRpc.listFilesystemRoots>>>({
    source,
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
