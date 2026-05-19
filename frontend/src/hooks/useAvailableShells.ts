import type { Accessor } from 'solid-js'
import { createEffect, createSignal, on, untrack } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { createGuardedFetch } from '~/hooks/createGuardedFetch'

export interface UseAvailableShellsArgs {
  orgId: string
  workspaceId: string
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
   * Manual retry hook for the current source. The worker-change effect
   * only fires on a workerId transition, so a transient failure on the
   * current worker would otherwise leave the dialog stuck with an empty
   * shell list and no recovery path until the user picked a different
   * worker. Refresh re-runs the fetch against the current source; no-op
   * when the source is null (the gate said "don't fetch yet").
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
 * - `source` returns the fetch args or `null` to skip. The hook fetches
 *   the first time `source` returns a non-null value and re-fetches on
 *   `workerId` change. While `source` returns null, the cached shells
 *   remain in place so a caller that gates the source on a mode toggle
 *   (e.g. show shell list only in worktree-terminal mode) doesn't
 *   re-issue the RPC when re-toggling.
 * - `defaultShell` returns the server-reported default, falling back to
 *   the first shell in the list, falling back to ''.
 * - The user override resets whenever `workerId` changes, so picking a
 *   different worker doesn't carry a stale shell selection across the
 *   worker change.
 */
export function useAvailableShells(
  source: Accessor<UseAvailableShellsArgs | null>,
  onError?: (err: unknown) => void,
): UseAvailableShellsResult {
  const [shells, setShells] = createSignal<string[]>([])
  const [serverDefault, setServerDefault] = createSignal('')
  const [userSelectedShell, setUserSelectedShell] = createSignal<string | null>(null)

  // `lastLoadedWorkerId` advances only on a SUCCESSFUL listAvailableShells
  // — a failed fetch leaves it unchanged so the next reactive tick with
  // the same workerId can retry instead of short-circuiting on a stale
  // sentinel. Assigning before the fetch (as an earlier revision did)
  // would lock the dialog out of recovering from a transient failure
  // until the user switched workers and back.
  let lastLoadedWorkerId = ''

  const fetcher = createGuardedFetch<UseAvailableShellsArgs, Awaited<ReturnType<typeof workerRpc.listAvailableShells>>>({
    fetch: args => workerRpc.listAvailableShells(args.workerId, args),
    applySuccess: (resp, args) => {
      setShells(resp.shells)
      setServerDefault(resp.defaultShell)
      lastLoadedWorkerId = args.workerId
    },
    onError: (err) => {
      onError?.(err)
      setShells([])
      setServerDefault('')
    },
  })

  // Track `source()?.workerId` rather than the source accessor itself.
  // Caller closures typically build a fresh args object each tick
  // (`{ orgId, workspaceId, workerId }`), so `on(source, ...)` would
  // re-fire the effect on every identity change upstream (e.g. an
  // `org.orgId()` tick) even when the workerId — the only field that
  // gates the fetch — is unchanged. Tracking the workerId scalar means
  // identity churn that doesn't change worker stays a no-op tick on the
  // memo, not a full effect run.
  //
  // orgId / workspaceId are passed through to the RPC for
  // authorization but the worker's ListAvailableShells handler ignores
  // them (shells are per-worker, not per-workspace), so an org or
  // workspace change with the same worker does not warrant a refetch.
  const workerIdFromSource = (): string | null => source()?.workerId ?? null
  createEffect(on(workerIdFromSource, (workerId) => {
    if (!workerId)
      return
    if (workerId === lastLoadedWorkerId)
      return
    // Worker changed (or the previous workerId never resolved) — clear
    // the cached shells / serverDefault and the explicit override so
    // shell() falls back to '' while the new fetch is in flight. Without
    // this clear, the dialog reports the previous worker's default
    // (effectively a leaked selection) during the transition window and
    // an isTerminalCreateDisabled gate that only checks shell() != ''
    // would let the user submit with a shell the new worker may not
    // even have.
    setShells([])
    setServerDefault('')
    setUserSelectedShell(null)
    // Pull the rest of the fetch args out of `source()` untracked (the
    // on() already subscribes via `workerIdFromSource`).
    const args = untrack(source)
    if (args === null)
      return
    void fetcher.run(args)
  }))

  const defaultShell = () => {
    const s = shells()
    return serverDefault() || (s.length > 0 ? s[0] : '')
  }
  const shell = () => userSelectedShell() ?? defaultShell()

  const refresh = async (): Promise<void> => {
    const args = untrack(source)
    if (args === null)
      return
    // Don't clear lastLoadedWorkerId here; the source-driven effect
    // only consults it on a workerId TRANSITION, so a manual refresh
    // against the current worker just re-fetches and re-stamps it on
    // success (or leaves it untouched on failure, preserving the
    // retry-allowed invariant).
    await fetcher.run(args)
  }

  return {
    shells,
    defaultShell,
    shell,
    setShell: setUserSelectedShell,
    loading: fetcher.loading,
    refresh,
  }
}
