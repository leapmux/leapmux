import type { Accessor } from 'solid-js'
import type { WorkerScopedArgs } from '~/hooks/createWorkerScopedList'
import { createSignal } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { createWorkerScopedList } from '~/hooks/createWorkerScopedList'

export interface UseAvailableShellsArgs {
  workerId: string
}

interface UseAvailableShellsResult {
  shells: Accessor<string[]>
  defaultShell: Accessor<string>
  /** Effective shell: user override when set, else the resolved default. */
  shell: Accessor<string>
  /** Pass `null` to clear the override and re-follow the default. */
  setShell: (v: string | null) => void
  loading: Accessor<boolean>
  /**
   * Manual retry hook for the current worker. The worker-change effect
   * only fires on a workerId transition, so a transient failure on the
   * current worker would otherwise leave the dialog stuck with an empty
   * shell list and no recovery path until the user picked a different
   * worker. Refresh re-runs the fetch against the current worker, whatever
   * `enabled` says; no-op when there is no worker.
   */
  refresh: () => Promise<void>
}

/**
 * Reactive wrapper around the listAvailableShells worker RPC plus a
 * user-override slot.
 *
 * Implemented with plain signals + an effect rather than
 * `createResource` — the router's Suspense boundary unmounts the entire
 * route while a resource is loading, flashing blank under any dialog
 * that reads it during initial fetch.
 *
 * - `workerId` says WHICH worker; `enabled` says whether to ask. The hook
 *   fetches the first time `enabled` is true for a worker and re-fetches on
 *   `workerId` change. While `enabled` is false the cached shells remain in
 *   place, so a caller that gates on a mode toggle (e.g. show the shell list
 *   only in worktree-terminal mode) doesn't re-issue the RPC when
 *   re-toggling -- but a WORKER change still clears, whatever the gate says.
 * - `defaultShell` returns the server-reported default, falling back to
 *   the first shell in the list, falling back to ''.
 * - The user override resets whenever `workerId` changes, so picking a
 *   different worker doesn't carry a stale shell selection across the
 *   worker change.
 * - `onError` and `onLoaded` come as a PAIR. A caller that reports a failed
 *   load somewhere durable — NewTerminalDialog writes the dialog's error
 *   banner — must be able to withdraw the report when a later load succeeds,
 *   or the Refresh-shells button repopulates the menu and arms Create while
 *   the banner still says the load failed. `onLoaded` is that withdrawal.
 */
export function useAvailableShells(
  workerId: Accessor<string>,
  enabled: Accessor<boolean>,
  onError?: (err: unknown) => void,
  onLoaded?: () => void,
): UseAvailableShellsResult {
  const [shells, setShells] = createSignal<string[]>([])
  const [serverDefault, setServerDefault] = createSignal('')
  const [userSelectedShell, setUserSelectedShell] = createSignal<string | null>(null)

  // Every rule about WHEN to fetch, when to retry and when to clear lives in
  // `createWorkerScopedList`. This hook keeps only the state it stores: the
  // server default beside the list, and the user's explicit override.
  const list = createWorkerScopedList<WorkerScopedArgs, Awaited<ReturnType<typeof workerRpc.listAvailableShells>>>({
    workerId,
    enabled,
    args: () => ({}),
    fetch: args => workerRpc.listAvailableShells(args.workerId, args),
    applySuccess: (resp) => {
      setShells(resp.shells)
      setServerDefault(resp.defaultShell)
      onLoaded?.()
    },
    // The override is cleared with the list, so `shell()` falls back to '' while
    // the new fetch is in flight. Without it the dialog reports the PREVIOUS
    // worker's default during the transition -- a leaked selection -- and a
    // create gate that only checks `shell() !== ''` would let the user submit a
    // shell the new worker may not have.
    clear: () => {
      setShells([])
      setServerDefault('')
      setUserSelectedShell(null)
    },
    onError,
  })

  const defaultShell = () => {
    const s = shells()
    return serverDefault() || (s.length > 0 ? s[0] : '')
  }
  const shell = () => userSelectedShell() ?? defaultShell()

  return {
    shells,
    defaultShell,
    shell,
    setShell: setUserSelectedShell,
    loading: list.loading,
    refresh: list.refresh,
  }
}
