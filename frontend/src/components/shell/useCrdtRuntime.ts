import type { UserCrdtState } from '~/generated/leapmux/v1/user_crdt_pb'
import type { Projection } from '~/lib/crdt'
import type { createActiveClientStore } from '~/lib/presence/activeClient'
import { createEffect, createMemo, createSignal } from 'solid-js'
import { showWarnToast } from '~/components/common/Toast'
import { KEY_CLIENT_ID, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { createProjectionMemo, HLCClock, PendingOpsManager, setCRDTBridge } from '~/lib/crdt'
import { randomUUID } from '~/lib/idGenerator'
import { createOpsSubmitter } from './useOpsSubmitter'
import { useUserEvents } from './useUserEvents'

/**
 * The CRDT runtime: pending manager, clock, user-events subscription, op
 * submitter, and the global bridge that lets the imperative stores emit
 * batches without threading dependencies through their constructors.
 *
 * Lifted out of `AppShell` because these pieces are one unit held together by a
 * contract that was previously stated in three separate comments scattered
 * through a 1200-line component: `PendingOpsManager` mutates `speculativeState`
 * IN PLACE, so its identity never changes, so `crdtState` must carry
 * `equals: false`, so `pendingVersion` is the only Solid-observable signal that
 * a mutation happened. Every one of those depends on the others; none of them
 * has anything to do with routing, git status, or the tab join, which is what
 * the rest of that component owns.
 *
 * Everything returned is what something OUTSIDE the runtime genuinely needs.
 * `pendingMgr`, `pendingVersion` and `opsSubmitter` deliberately stay private:
 * the stores reach the submitter through the bridge, not by reference.
 */
export interface UseCrdtRuntimeOpts {
  /** Authenticated user id; empty until the session restores. */
  userId: () => string
  /** Which workspace new ops are attributed to. Read live by the bridge. */
  getWorkspaceId: () => string | null
  /**
   * Per-(workspace_id) active-client tracker, fed by PresenceUpdate events off
   * this same subscription. Owned by the caller because the turn-end ding gate
   * also consults it.
   */
  activeClient: ReturnType<typeof createActiveClientStore>
  /**
   * Called when a workspace is created / archived / deleted anywhere.
   *
   * Late-bound by the caller: `useWorkspaceLoader` is instantiated further down
   * the component, so this fires into a no-op until then. That is safe because
   * the loader's own `onMount` seeds the initial `listWorkspaces`.
   */
  onWorkspaceLifecycleChanged: () => void
}

export interface CrdtRuntime {
  /** Raw user-wide CRDT state. `equals: false` — see the note above. */
  crdtState: () => UserCrdtState | null
  /** The projection derived from it, covering every workspace at once. */
  projection: () => Projection | null
  userEvents: ReturnType<typeof useUserEvents>
  /** Hub-reported identity of this subscriber; empty until bootstrap. */
  effectiveClientId: () => string
  /** Stable per-session client id used as the HLC author and op_id salt. */
  ownClientId: string
}

export function useCrdtRuntime(opts: UseCrdtRuntimeOpts): CrdtRuntime {
// Local CRDT pending manager + clock. Constructed lazily once the
// user id is known; useUserEvents seeds the bootstrap from
// UserMaterialized and useOpsSubmitter drives commits.
  const [pendingMgr, setPendingMgr] = createSignal<PendingOpsManager | null>(null)
  // Reactive version counter that bumps on every PendingOpsManager
  // state mutation (submit / consumeRemote / consumeBatch* /
  // consumeEntity* / bootstrap). Subscribed by `bridge.speculativeState`
  // so memoized projections in the layout / floating-window / tab
  // stores re-derive when ops land. The PendingOpsManager mutates
  // its UserCrdtState in place; this signal is how Solid observes it.
  const [pendingVersion, setPendingVersion] = createSignal(0)

  // The single source of truth for everything the CRDT carries, covering EVERY
  // workspace at once. `project()` shares one `registeredRoots` +
  // `buildChildIndex` pass across all of them, so this costs one pass per tick
  // rather than one per workspace.
  //
  // `equals: false` on the state accessor is load-bearing: PendingOpsManager
  // mutates `speculativeState` in place, so its identity never changes and a
  // default memo would swallow every update after the first.
  const crdtState = createMemo(
    () => {
      pendingVersion()
      return pendingMgr()?.state.speculativeState ?? null
    },
    undefined,
    { equals: false },
  )
  // The `equals: false` above means the projection memo BODY runs on every CRDT
  // tick -- ~2x the frame rate while a tile is dragged, since each frame's
  // optimistic `submit` is followed by the `BatchCommitted` that triggers
  // `recomputeSpeculative`. Running the body is cheap either way; what was not
  // is that every one of those ticks used to hand the WHOLE APP a fresh graph,
  // so a drag in one workspace re-derived every other workspace's tree and every
  // tab in the account. `createProjectionMemo`'s cache reuses whatever the tick
  // left alone and returns the IDENTICAL `Projection` object when nothing
  // observable moved, so the memo's default `equals` stops propagation dead. See
  // `ProjectionCache` for why identity comparison against the live records is
  // sound rather than a remembered dirty set.
  const projection = createProjectionMemo(crdtState)

  // Stable client_id for this browser session. Used as the HLC author
  // for op-stamping and as the `op_id` salt — the local random nanoid
  // gives every pending op a deterministic identity for echo dedup.
  //
  // **The active-client gate does NOT compare against this.** The hub
  // identifies subscribers by session id / bearer-token id (so it can
  // refuse cross-tab spoofing), which never matches the local nanoid.
  // The hub returns the subscriber's effective identity in
  // `UserMaterialized.subscriber_client_id`; the gate compares
  // `activeClient.activeFor(wsId)` against `effectiveClientId()`.
  const ownClientId = (() => {
    const cur = sessionStorageGet<string>(KEY_CLIENT_ID)
    if (cur)
      return cur
    const fresh = `c-${randomUUID()}`
    sessionStorageSet(KEY_CLIENT_ID, fresh)
    return fresh
  })()

  // Effective identity reported by the hub via UserMaterialized; the
  // active-client gate compares broadcast `active_client_id` against
  // this. Empty until bootstrap; gate treats empty as "unknown — allow"
  // so a sole client plays its ding even before the first heartbeat
  // broadcast settles.
  const [effectiveClientId, setEffectiveClientId] = createSignal('')
  const bumpPending = () => setPendingVersion(v => v + 1)
  createEffect(() => {
    const uid = opts.userId()
    if (!uid) {
      setPendingMgr(null)
      return
    }
    setPendingMgr(new PendingOpsManager(uid, new HLCClock(ownClientId), bumpPending))
  })

  // Open the per-user `/ws/userevents` subscription once the user id is
  // known. The hook stays live across workspace switches; per-
  // workspace stores slice the materialized state instead.
  const userEvents = useUserEvents({
    userId: () => opts.userId(),
    activeClient: opts.activeClient,
    pending: () => pendingMgr(),
    onWorkspaceLifecycleChanged: () => opts.onWorkspaceLifecycleChanged(),
    onSubscriberClientId: id => setEffectiveClientId(id),
    onPendingDropped: () => {
    // EntityRemoved dropped at least one pending op (a redacted
    // entity left the visible set with a local mutation still in
    // flight). Surface a warn-toast so the user understands their
    // recent action didn't take effect.
      showWarnToast('A pending change was discarded because the affected item left your view.')
    },
    onFatalClose: () => {
    // The user-events stream closed with a terminal code (e.g. the session
    // expired / access was revoked), so useUserEvents stopped retrying rather
    // than loop. Tell the user to reload instead of silently going stale.
      showWarnToast('Live updates disconnected. Reload the page to reconnect.')
    },
  })

  // 16ms aggregator for op submission. Stores call into the CRDT
  // bridge below; the submitter handles the SubmitOps RPC + per-
  // batch commit/reject result dispatch, including:
  //   - epoch_required → reconnect + retry (refresh currentEpoch)
  //   - stale_epoch → reconnect + warn-toast (no auto-retry)
  //   - any other rejection → drop + warn-toast keyed by reason
  //   - transport timeout → retry same op_ids (principal-aware dedup)
  const opsSubmitter = createOpsSubmitter({
    pending: () => pendingMgr(),
    reconnect: () => userEvents.reconnect(),
  })

  // Wire the global CRDT bridge so the imperative stores can emit op
  // batches without threading every dependency through their
  // constructors. Re-installed on every reactive change to the
  // pending manager / user id.
  createEffect(() => {
    const mgr = pendingMgr()
    const uid = opts.userId()
    if (!mgr || !uid) {
      setCRDTBridge(null)
      return
    }
    setCRDTBridge({
      workspaceId: () => opts.getWorkspaceId(),
      enqueue: (batch) => {
        opsSubmitter.enqueue(batch)
        return batch.batchId
      },
      clock: () => mgr.clock,
      originClientId: () => ownClientId,
      speculativeState: () => {
      // Read the version signal so memoized consumers re-derive on
      // every mutation. The manager updates its state in place; the
      // version bump is the only Solid-observable signal.
        pendingVersion()
        return mgr.state.speculativeState
      },
    })
  })
  return { crdtState, projection, userEvents, effectiveClientId, ownClientId }
}
