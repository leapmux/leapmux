import type { Component } from 'solid-js'
import type { KeyPinConfirmState } from './AppShellDialogs'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { DialogState } from '~/hooks/createDialogState'
import { createEffect, createSignal, onCleanup, Show } from 'solid-js'
import { workerClient } from '~/api/clients'
import { channelManager, setConfirmKeyPin } from '~/api/workerRpc'
import { AddTunnelDialog } from '~/components/workers/AddTunnelDialog'
import { RegisterWorkerDialog } from '~/components/workers/RegisterWorkerDialog'
import { WorkerSettingsDialog } from '~/components/workers/WorkerSettingsDialog'
import { HubControlEvent } from '~/generated/leapmux/v1/channel_pb'
import { createIdentityCache } from '~/lib/identityCache'
import { createTunnelStore } from '~/stores/tunnel.store'
import { createWorkerChannelStatusStore } from '~/stores/workerChannelStatus.store'
import { workerInfoStore } from '~/stores/workerInfo.store'

/**
 * The worker registry: the list, its refresh triggers, and the three dialogs
 * that mutate it.
 *
 * Lifted out of `AppShell` because it shares nothing with the rest of that
 * component — no tabs, no layout, no projection, no CRDT. Its only input is the
 * authenticated user id. Leaving it inline meant a change to how workers are
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
  workerInfoFn: typeof workerInfoStore.workerInfo
  channelStatusFn: ReturnType<typeof createWorkerChannelStatusStore>['getStatus']
  tunnelStore: ReturnType<typeof createTunnelStore>
  openAddTunnel: (worker: Worker) => void
  openWorkerSettings: (worker: Worker) => void
  openRegisterWorker: () => void
  Dialogs: Component
}

export function useWorkerSection(opts: UseWorkerSectionOpts): WorkerSection {
  const workerChannelStatusStore = createWorkerChannelStatusStore(channelManager)
  const [workers, setWorkers] = createSignal<Worker[]>([])
  const [deregisterTarget, setDeregisterTarget] = createSignal<Worker | null>(null)
  const [addTunnelTarget, setAddTunnelTarget] = createSignal<Worker | null>(null)
  const [showRegisterWorker, setShowRegisterWorker] = createSignal(false)
  const tunnelStore = createTunnelStore()

  // listWorkers() returns freshly-deserialized objects on every call.
  // Stabilize identity by id so the sidebar's <For> doesn't unmount and
  // remount every worker row on each refresh / WORKERS_CHANGED push.
  const workerIdentity = createIdentityCache<Worker>({ keyOf: w => w.id })

  async function fetchWorkers() {
    if (!opts.getUserId())
      return
    try {
      const resp = await workerClient.listWorkers({})
      const stable = workerIdentity.stabilize(resp.workers)
      setWorkers(stable)
      for (const w of stable) {
        if (w.online)
          workerInfoStore.fetchWorkerInfo(w.id)
      }
    }
    catch {
      // Best effort — sidebar will show an empty workers list.
    }
  }

  // Fetch when the authenticated user is known.
  createEffect(() => {
    opts.getUserId() // track
    void fetchWorkers()
  })

  // Re-fetch when the Hub sends a WorkersChanged control frame.
  //
  // `channelManager` is a module-level singleton that outlives this owner, so
  // the unsubscribe has to be honoured. AppShell really does remount inside one
  // page lifetime — logging out navigates to /login client-side and logging
  // back in navigates to /, which unmounts and remounts it through AuthGuard —
  // and a dropped unsubscribe leaves the previous mount's listener attached.
  // Every later WORKERS_CHANGED frame would then fan out one `listWorkers` plus
  // a `fetchWorkerInfo` per online worker for each stale mount.
  onCleanup(channelManager.onHubControl((frame) => {
    if (frame.events.includes(HubControlEvent.WORKERS_CHANGED))
      void fetchWorkers()
  }))

  // Register the E2EE channel callback (a module-level singleton in workerRpc).
  //
  // Unregistered on dispose for the same reason the `onHubControl` subscription
  // above is, and with a sharper failure: the retained registration leaves the
  // singleton holding a closure over the DEAD mount's dialog. A TOFU mismatch in
  // the gap between unmount and remount would open a dialog nothing renders, so
  // its `resolve` is never called — and `KeyPinStore.resolve` awaits that with
  // no timeout while `enqueueConfirm` queues every later prompt behind it. One
  // mismatch would deadlock key-pinning for the rest of the page. Restoring the
  // fail-closed default makes that window reject instead of hang.
  onCleanup(setConfirmKeyPin((workerId, expectedFingerprint, actualFingerprint) =>
    new Promise((resolve) => {
      opts.keyPinConfirmDialog.open({ workerId, expectedFingerprint, actualFingerprint, resolve })
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

      <Show when={addTunnelTarget()}>
        {target => (
          <AddTunnelDialog
            workerId={target().id}
            onClose={() => setAddTunnelTarget(null)}
            onCreated={() => setAddTunnelTarget(null)}
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
    workerInfoFn: workerInfoStore.workerInfo,
    channelStatusFn: workerChannelStatusStore.getStatus,
    tunnelStore,
    openAddTunnel: setAddTunnelTarget,
    openWorkerSettings: setDeregisterTarget,
    openRegisterWorker: () => setShowRegisterWorker(true),
    Dialogs,
  }
}
