import type { Component } from 'solid-js'
import type { KeyPinConfirmState } from './AppShellDialogs'
import type { AddTunnelTarget } from '~/components/workers/AddTunnelDialog'
import type { WorkerTrustScope } from '~/context/AuthContext'
import type { Worker } from '~/generated/proto/leapmux/v1/worker_pb'
import type { DialogState } from '~/hooks/createDialogState'
import { equals } from '@bufbuild/protobuf'
import { createEffect, createSignal, onCleanup, Show, untrack } from 'solid-js'
import { isTunnelAvailable, platformBridge } from '~/api/platformBridge'
import { channelManager, setConfirmKeyPin } from '~/api/workerRpc'
import { AddTunnelDialog } from '~/components/workers/AddTunnelDialog'
import { RegisterWorkerDialog } from '~/components/workers/RegisterWorkerDialog'
import { WorkerSettingsDialog } from '~/components/workers/WorkerSettingsDialog'
import { TAURI_EVENT_TUNNELS_CHANGED } from '~/generated/contracts/desktop'
import { WorkerSchema } from '~/generated/proto/leapmux/v1/worker_pb'
import { createIdentityCache } from '~/lib/identityCache'
import { listAllWorkers } from '~/lib/listAllWorkers'
import { createLogger } from '~/lib/logger'
import { createTunnelStore } from '~/stores/tunnel.store'
import { createWorkerChannelStatusStore } from '~/stores/workerChannelStatus.store'
import { workerInfoStore } from '~/stores/workerInfo.store'

const log = createLogger('workerSection')

// Recover a missed native hint while a visible sidebar shows an active tunnel.
const TUNNEL_REFRESH_INTERVAL_MS = 30_000

/**
 * The worker registry: the list, its refresh triggers, and the three dialogs
 * that mutate it.
 *
 * Lifted out of `AppShell` because it shares nothing with the rest of that
 * component — no tabs, no layout, no projection, no CRDT. It receives the user
 * ID for the registry and the identity lifetime for tunnels. Leaving it inline
 * meant a change to how workers are
 * listed landed in the same 1300-line file that owns the CRDT bootstrap, the
 * tab join, git-status orchestration and routing policy.
 *
 * Returns the sidebar wiring plus a `Dialogs` component, because the dialogs
 * are the other half of this state: `WorkerSettingsDialog`'s `onDeregistered`
 * has to splice the list, so separating them would hand the caller a setter it
 * has no other use for.
 */
export interface UseWorkerSectionOpts {
  getUserId: () => string
  workerTrustScope: () => WorkerTrustScope
  /**
   * Where a TOFU key-pin mismatch is surfaced. Owned by the caller because it
   * lives in the shared `dialogs` record every AppShell dialog is threaded
   * through, and `setConfirmKeyPin` is a module-level singleton in `workerRpc`
   * that must be registered exactly once.
   */
  keyPinConfirmDialog: DialogState<KeyPinConfirmState>
  // No `clearRepoGitForWorker`. See `onDeregistered` below for why the
  // repo-keyed git store outlives a deregistration.
}

export interface WorkerSection {
  workers: () => Worker[]
  /**
   * Re-fetches the worker list. Coalescing: at most one fetch runs, and any
   * number of calls during it cause exactly one more fetch after it.
   */
  refetchWorkers: () => void
  workerInfoFn: typeof workerInfoStore.workerInfo
  channelStatusFn: ReturnType<typeof createWorkerChannelStatusStore>['getStatus']
  tunnelStore: ReturnType<typeof createTunnelStore>
  openAddTunnel: (target: AddTunnelTarget) => void
  openWorkerSettings: (worker: Worker) => void
  openRegisterWorker: () => void
  Dialogs: Component
}

export function useWorkerSection(opts: UseWorkerSectionOpts): WorkerSection {
  const workerChannelStatusStore = createWorkerChannelStatusStore(channelManager)
  const [workers, setWorkers] = createSignal<Worker[]>([])
  const [deregisterTarget, setDeregisterTarget] = createSignal<Worker | null>(null)
  const [addTunnelTarget, setAddTunnelTarget] = createSignal<AddTunnelTarget | null>(null)
  const [showRegisterWorker, setShowRegisterWorker] = createSignal(false)
  const tunnelStore = createTunnelStore(opts.workerTrustScope)

  // listAllWorkers() returns freshly-deserialized objects on every call.
  // Stabilize identity by id so the sidebar's <For> doesn't unmount and
  // remount every worker row on each refetch, which a `workersChanged` user
  // event or a user-events bootstrap starts. By value: `createdAt` and
  // `lastSeenAt` are nested Timestamp messages that every reply decodes afresh,
  // so the default shallow comparison never matched.
  const workerIdentity = createIdentityCache<Worker>({
    keyOf: w => w.id,
    equals: (cached, fresh) => equals(WorkerSchema, cached, fresh),
  })

  async function fetchWorkers() {
    if (!opts.getUserId())
      return
    try {
      const stable = workerIdentity.stabilize(await listAllWorkers())
      setWorkers(stable)
      for (const w of stable) {
        if (w.online)
          workerInfoStore.fetchWorkerInfo(w.id)
      }
    }
    catch (err) {
      // Best effort: a failed read keeps the last successful list, which is
      // empty before the first one. The next `workersChanged` event or
      // user-events bootstrap fetches again.
      log.warn('Failed to list the workers; keeping the last list', err)
    }
  }

  // One fetch in flight plus at most one queued behind it. A burst of
  // `workersChanged` events -- a worker deregistering while another registers,
  // or a bootstrap landing beside a live event -- then costs two worker
  // listings (`listAllWorkers` reads, one `listWorkers` call per page), and the
  // second one still reads the state after every event in the burst.
  let fetchInFlight = false
  let fetchQueued = false
  function refetchWorkers(): void {
    if (fetchInFlight) {
      fetchQueued = true
      return
    }
    fetchInFlight = true
    void (async () => {
      try {
        do {
          fetchQueued = false
          await fetchWorkers()
        } while (fetchQueued)
      }
      finally {
        fetchInFlight = false
      }
    })()
  }

  // Fetch when the authenticated user is known.
  createEffect(() => {
    opts.getUserId() // track
    refetchWorkers()
  })

  if (isTunnelAvailable()) {
    const [registered, setRegistered] = createSignal(false)
    const [visible, setVisible] = createSignal(document.visibilityState === 'visible')
    let disposed = false
    let unlisten: (() => void) | undefined

    function refreshTunnels(scope: WorkerTrustScope, mode: 'coalesce' | 'invalidate' = 'coalesce'): void {
      if (disposed || !scope.userId || scope.signal.aborted)
        return
      void tunnelStore.refresh(scope, mode).catch((err: unknown) => {
        if (!disposed && !scope.signal.aborted)
          log.warn('Failed to list the tunnels', err)
      })
    }

    // The hint carries no rows. Wait until its listener is attached before
    // reading the initial snapshot, then invalidate a list already in flight.
    void platformBridge.onEvent(TAURI_EVENT_TUNNELS_CHANGED, () => {
      untrack(() => {
        if (registered())
          refreshTunnels(opts.workerTrustScope(), 'invalidate')
      })
    }).then((disposeListener) => {
      if (disposed)
        disposeListener()
      else
        unlisten = disposeListener
    }).catch((err: unknown) => {
      if (!disposed)
        log.warn('Failed to listen for tunnel changes', err)
    }).finally(() => {
      if (!disposed)
        setRegistered(true)
    })

    createEffect(() => {
      const scope = opts.workerTrustScope()
      if (registered())
        refreshTunnels(scope)
    })

    const onVisibilityChange = () => {
      setVisible(document.visibilityState === 'visible')
      if (registered() && visible())
        refreshTunnels(opts.workerTrustScope(), 'invalidate')
    }
    document.addEventListener('visibilitychange', onVisibilityChange)
    createEffect(() => {
      if (!registered() || !visible() || !tunnelStore.tunnels().some(tunnel => !tunnel.keyMismatch))
        return
      const timer = window.setInterval(() => {
        refreshTunnels(opts.workerTrustScope())
      }, TUNNEL_REFRESH_INTERVAL_MS)
      onCleanup(() => window.clearInterval(timer))
    })

    onCleanup(() => {
      disposed = true
      unlisten?.()
      document.removeEventListener('visibilitychange', onVisibilityChange)
    })
  }

  createEffect(() => {
    const scope = opts.workerTrustScope()
    const target = addTunnelTarget()
    if (target && (target.scope !== scope || target.scope.signal.aborted))
      setAddTunnelTarget(null)
  })

  const closeAddTunnel = (target: AddTunnelTarget): void => {
    if (addTunnelTarget() === target)
      setAddTunnelTarget(null)
  }

  // The singleton registration retires with this owner and aborts its prompt.
  onCleanup(setConfirmKeyPin((workerId, expectedFingerprint, actualFingerprint, signal) =>
    new Promise((resolve, reject) => {
      signal.throwIfAborted()
      const dialog = opts.keyPinConfirmDialog
      let state: KeyPinConfirmState
      const close = () => {
        if (dialog.value() === state)
          dialog.close()
      }
      const abort = () => {
        close()
        reject(signal.reason)
      }
      state = {
        workerId,
        expectedFingerprint,
        actualFingerprint,
        resolve: (decision) => {
          if (signal.aborted || dialog.value() !== state)
            return
          signal.removeEventListener('abort', abort)
          close()
          resolve(decision)
        },
      }
      signal.addEventListener('abort', abort, { once: true })
      dialog.open(state)
    }),
  ))

  const Dialogs: Component = () => (
    <>
      <Show when={deregisterTarget()}>
        {target => (
          <WorkerSettingsDialog
            worker={target()}
            onClose={() => setDeregisterTarget(null)}
            // The repo-keyed git store is NOT cleared here. Deregistration
            // removes the worker from this list, and nothing removes that
            // worker's tab rows: they keep `gitToplevel`, and the sidebar groups
            // a tab by that field while it reads the branch label from the
            // store. Clearing therefore left every tab of the worker under its
            // repo with no branch name, for the life of the page, with no way
            // back -- the same defect the worker-offline sweep used to cause.
            //
            // An entry is last-known working-tree state. The rows it labels
            // outlive the deregistration, so it should too.
            onDeregistered={() => {
              const id = target().id
              setWorkers(prev => prev.filter(w => w.id !== id))
              setDeregisterTarget(null)
            }}
          />
        )}
      </Show>

      <Show when={addTunnelTarget()} keyed>
        {target => (
          <AddTunnelDialog
            target={target}
            onClose={() => closeAddTunnel(target)}
            onCreated={() => closeAddTunnel(target)}
          />
        )}
      </Show>

      <Show when={showRegisterWorker()}>
        <RegisterWorkerDialog onClose={() => setShowRegisterWorker(false)} />
      </Show>
    </>
  )

  return {
    workers,
    refetchWorkers,
    workerInfoFn: workerInfoStore.workerInfo,
    channelStatusFn: workerChannelStatusStore.getStatus,
    tunnelStore,
    openAddTunnel: (target) => {
      if (target.scope === opts.workerTrustScope() && !target.scope.signal.aborted)
        setAddTunnelTarget(target)
    },
    openWorkerSettings: setDeregisterTarget,
    openRegisterWorker: () => setShowRegisterWorker(true),
    Dialogs,
  }
}
