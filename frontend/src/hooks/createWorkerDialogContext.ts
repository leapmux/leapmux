import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import { createEffect, createSignal, getOwner, on, onCleanup, onMount } from 'solid-js'
import { workerClient } from '~/api/clients'
import { formatErrorMessage } from '~/lib/errors'
import { createIdentityCache } from '~/lib/identityCache'
import { resetOnlinePrefetch, shouldPrefetchOnline, workerInfoStore } from '~/stores/workerInfo.store'

export interface WorkerDialogContextOptions {
  preselectedWorkerId?: string
  defaultWorkingDir?: string
  /**
   * When set, the dialog is locked to this worker — `listWorkers` is
   * skipped (no fleet round-trip, no per-worker system-info fan-out) and
   * only this worker's system info is fetched on demand. Use for
   * dialogs that don't render a worker selector (e.g. ChangeBranchDialog,
   * DeleteBranchDialog).
   */
  singleWorkerId?: string
  /**
   * Sink for the `listWorkers` failure path. The dialog wires this to
   * its `useDialogSubmit` error setter so a fleet-listing failure shows
   * up in the same error slot as a submit failure. Receives a fully
   * formatted, user-facing string; defaults to a no-op when omitted (the
   * caller has chosen not to surface load errors).
   */
  onError?: (message: string) => void
}

/**
 * Worker-targeting dialog context: bundles worker selection and the
 * working directory for dialogs that target a single worker. Git-mode
 * state lives next door in `useGitModeState`; `useWorkerDialog` composes
 * the two as siblings so each concern stays independently testable.
 *
 * Org and worker-info access are NOT exposed via the returned object:
 * dialogs call `useOrg()` directly (they're already inside a Solid
 * component scope) and read the {@link workerInfoStore} singleton
 * directly when they need worker metadata beyond `getHomeDir()`. The
 * single convenience kept here is `getHomeDir()`, bound to the
 * currently-selected workerId so dialogs that pass it through to
 * `homeDir` props don't have to thread the worker id alongside.
 *
 * Submit / load errors are NOT owned here — callers pair this with
 * `useDialogSubmit` and pass its `setError` via `options.onError`, so
 * the dialog has one error sink shared across submit failures and
 * fleet-list failures.
 *
 * Lifecycle-scoped helpers live outside — `useGitPathInfo`,
 * `useAgentProviderSelection`, `useAvailableShells` — so each consumer
 * owns its own loading state and the spinner UX lives with the caller.
 */
export function createWorkerDialogContext(options: WorkerDialogContextOptions = {}) {
  // Workers come back as freshly-deserialized proto objects on every
  // listWorkers() call, even when nothing has changed. Stabilize the
  // object identity by id so the dialog's <For> doesn't unmount and
  // remount every row on each refresh.
  const workerIdentity = createIdentityCache<Worker>({
    keyOf: w => w.id,
  })
  const [workers, setWorkers] = createSignal<Worker[]>([])
  // singleWorkerId seeds workerId synchronously so dialogs locked to a
  // worker render against it from the first paint — no flash of empty
  // state while listWorkers would have been resolving.
  const initialWorkerId = options.singleWorkerId ?? ''
  const [workerId, setRawWorkerId] = createSignal(initialWorkerId)
  // Wrap the setter so dialogs constructed with singleWorkerId really
  // are locked: a stray setWorkerId('other') would otherwise fire the
  // workerInfo createEffect for an unintended worker and desync any
  // sibling state (PushBranchButton's props.workerId, the dispatch
  // baseArgs, etc.) that still reads from the caller's `props.workerId`.
  // Reject the write loudly so the violation surfaces during development
  // instead of silently corrupting the dialog's worker view.
  const setWorkerId = options.singleWorkerId !== undefined
    ? (id: string): void => {
        if (id !== options.singleWorkerId) {
          throw new Error(
            `createWorkerDialogContext: setWorkerId(${id!}) rejected — dialog is locked to ${options.singleWorkerId}`,
          )
        }
        setRawWorkerId(id)
      }
    : setRawWorkerId
  const [workingDir, setWorkingDir] = createSignal(options.defaultWorkingDir ?? '')
  const [workersRefreshing, setWorkersRefreshing] = createSignal(false)

  // Owner-scoped disposal guard. A dialog dismissed before listWorkers
  // resolves would otherwise still write setWorkers / setWorkerId on a
  // disposed scope (Solid logs a 'computation outside reactive context'
  // warning). All the other RPCs in this PR route through
  // createGuardedFetch's owner-cleanup abort; this is the one that
  // doesn't, so guard it manually.
  let disposed = false
  if (getOwner()) {
    onCleanup(() => {
      disposed = true
    })
  }

  // Generation counter for fetchWorkers concurrency. The onMount fetch
  // and refreshWorkers can overlap (refresh button enabled before
  // refreshWorkers' setWorkersRefreshing fires, hotkey-driven refresh,
  // re-mount races), and listWorkers has no AbortController plumbing.
  // Without the counter, the LATER-resolving promise's setWorkers wins
  // regardless of which call started later — a 10s-stalled mount fetch
  // returning AFTER a fresh refresh would silently clobber the fresh
  // list. Bump the gen on every call; only the latest generation is
  // allowed to commit writes.
  let fetchGen = 0

  const fetchWorkers = async () => {
    const gen = ++fetchGen
    try {
      const resp = await workerClient.listWorkers({})
      if (disposed || gen !== fetchGen)
        return false
      const online = workerIdentity.stabilize(resp.workers.filter(b => b.online))
      setWorkers(online)
      if (online.length > 0 && !workerId()) {
        // Prefer the caller's preselected worker when it's online; fall
        // back to the first online worker otherwise.
        const preferred = options.preselectedWorkerId
          ? online.find(b => b.id === options.preselectedWorkerId)
          : undefined
        setWorkerId((preferred ?? online[0]).id)
      }
      return online.length > 0
    }
    catch (e) {
      if (disposed || gen !== fetchGen)
        return false
      options.onError?.(formatErrorMessage(e, 'Failed to load workers'))
      return false
    }
  }

  // Fetch only the selected worker's system info eagerly — that's what
  // DirectorySelector / GitOptions need for `homeDir` on the first paint.
  // Info for the rest of the online fleet is prefetched lazily by
  // `prefetchOnlineWorkerInfos` (wired to the WorkerSelector's first
  // focus/pointerdown) so a 10-worker fleet doesn't pay 10 E2EE handshakes
  // at dialog open just to populate dropdown labels the user may never see.
  //
  // Gate on `disposed` so a dialog closed during the E2EE handshake
  // doesn't keep the round-trip alive past dispose. fetchWorkerInfo is
  // module-scoped (writes into workerInfoStore) so a stray response
  // wouldn't crash anything, but the wasted handshake + the resulting
  // store write could roll back a fresher value cached by a parallel
  // dialog.
  createEffect(on(workerId, (id) => {
    if (id && !disposed)
      workerInfoStore.fetchWorkerInfo(id)
  }))

  // Idempotent guard for the lazy fleet prefetch. The "already fanned
  // out for this id" set lives at module scope in `workerInfo.store`,
  // so a second or third dialog opened during the same session reuses
  // the prior dialog's work instead of re-fanning the whole online
  // fleet on every WorkerSelector focus. Per-worker freshness is still
  // enforced inside `fetchWorkerInfo` (FRESH_TTL_MS), so a stale entry
  // can refresh independently of this gate.
  const prefetchOnlineWorkerInfos = () => {
    if (disposed)
      return
    for (const w of workers()) {
      if (w.id === workerId())
        continue
      if (shouldPrefetchOnline(w.id))
        workerInfoStore.fetchWorkerInfo(w.id)
    }
  }

  // Locked-dialog detection: use `!== undefined` rather than a falsy
  // check so an empty string (`singleWorkerId: ''`) still activates the
  // lock. The setter wrap above already uses `!== undefined` and would
  // throw on any non-empty id when locked to ''. If the truthy-check
  // path leaked through, fetchWorkers would run AND then attempt to
  // setWorkerId(online[0].id), which the throwing setter rejects —
  // crashing the dialog at mount with no actionable diagnostic. The
  // defensive parity makes the lock semantics unambiguous: "if the
  // caller supplied a value, even '', they meant lock."
  const locked = options.singleWorkerId !== undefined

  // Fetch on mount only. When the dialog is locked to a single worker
  // we skip the fleet listing entirely (no `listWorkers` round trip, no
  // dropdown to populate); the createEffect above still fetches that
  // worker's system info via the seeded `workerId`.
  onMount(async () => {
    if (!locked)
      await fetchWorkers()
  })

  const refreshWorkers = async () => {
    if (disposed)
      return
    setWorkersRefreshing(true)
    try {
      if (locked) {
        // No fleet listing to refresh; just refetch the locked worker's
        // system info in case anything (e.g. version) has changed.
        await workerInfoStore.fetchWorkerInfo(options.singleWorkerId!)
        return
      }
      // Manual refresh = "give me fresh data now." Clear the module-
      // level prefetch guard so the next WorkerSelector open re-fans
      // the fleet (other dialogs sharing the guard pick up the fresh
      // data on their next prefetch tick too).
      resetOnlinePrefetch()
      await fetchWorkers()
    }
    finally {
      // Skip the setSignal write if the owner disposed mid-flight;
      // otherwise Solid logs a "computation outside reactive context"
      // warning when the finally clause runs after cleanup.
      if (!disposed)
        setWorkersRefreshing(false)
    }
  }

  // Bound convenience: callers that pass the home dir to `homeDir` props
  // (DirectorySelector, GitOptions) read this without threading workerId
  // alongside. Tracks workerId reactively so a worker swap updates
  // downstream consumers when the new worker's info lands in the cache.
  const getHomeDir = () => workerInfoStore.getHomeDir(workerId())

  return {
    // Worker selection
    workerId,
    setWorkerId,
    workers,
    workersRefreshing,
    refreshWorkers,
    prefetchOnlineWorkerInfos,
    // Working dir
    workingDir,
    setWorkingDir,
    // Worker info (focused accessor; full store access is via the
    // workerInfoStore singleton import).
    getHomeDir,
  }
}

export type WorkerDialogContext = ReturnType<typeof createWorkerDialogContext>
