import type { UserCrdtState } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import type { WatchUserEvent } from '~/generated/proto/leapmux/v1/user_ops_pb'
import type { Projection } from '~/lib/crdt'
import type { CheckpointRecorder, CheckpointRecorderOptions, HydratedBase } from '~/lib/crdt/checkpointRecorder'
import type { HydrationPayload, PersistedBase } from '~/lib/crdt/hydrate'
import type { createActiveClientStore } from '~/lib/presence/activeClient'
import type { FatalCloseInfo } from '~/lib/wsCloseCodes'
import { createEffect, createMemo, createSignal, onCleanup } from 'solid-js'
import { channelManager } from '~/api/workerRpc'
import { showStickyWarnToast, showWarnToast } from '~/components/common/Toast'
import { createProjectionMemo, HLCClock, PendingOpsManager, setCRDTBridge } from '~/lib/crdt'
import { CHECKPOINT_OP_LOG_THRESHOLD, createCheckpointRecorder } from '~/lib/crdt/checkpointRecorder'
import { clearCheckpointAndOpLog, clearOwnerCheckpointAndOpLog, OWNER_TOUCH_INTERVAL_MS, sweepAbandonedCheckpoints, touchOwner } from '~/lib/crdt/checkpointStore'
import { createClientIdentity } from '~/lib/crdt/clientIdentity'
import { loadHydrationState } from '~/lib/crdt/hydrate'
import { fatalCloseMessage } from '~/lib/fatalCloseMessage'
import { createLogger } from '~/lib/logger'
import { createOpsSubmitter } from './useOpsSubmitter'
import { useUserEvents } from './useUserEvents'

const log = createLogger('useCrdtRuntime')

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
  /**
   * This tab's client id — the HLC author, the op_id salt, and the owner half
   * of the persisted checkpoint key.
   *
   * A signal because a tab duplicated from another (which clones sessionStorage,
   * and with it the stored id) re-mints after the liveness handshake in
   * `~/lib/crdt/clientIdentity` detects the collision. Readers must not cache
   * the value across that change.
   */
  ownClientId: () => string
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
  // Whether the persisted checkpoint + op-log have been loaded + replayed for
  // the current user (or the cold-start fallback has been taken). The WS open
  // effect in useUserEvents waits on this so the resume cursor resolves against
  // hydrated confirmedState, not empty state. False during hydration and on
  // logout; true once the manager is live.
  const [hydrated, setHydrated] = createSignal(false)

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
  // tick -- twice per confirmed mutation, since each optimistic `submit` is
  // followed by the `BatchCommitted` echo that triggers `recomputeSpeculative`.
  // Running the body is cheap either way; what was not
  // is that every one of those ticks used to hand the WHOLE APP a fresh graph,
  // so an edit in one workspace re-derived every other workspace's tree and every
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
  //
  // A SIGNAL, not a constant: sessionStorage is COPIED into a duplicated tab, so
  // two live tabs can start out holding the same id and clobbering each other's
  // checkpoint. createClientIdentity runs a BroadcastChannel liveness handshake
  // and re-mints in the duplicate when that happens; the hydration effect below
  // reads this, so a re-mint simply re-hydrates under the new id (a checkpoint
  // miss -> one full snapshot for the duplicate). See clientIdentity.ts.
  const identity = createClientIdentity()
  const ownClientId = identity.clientId
  onCleanup(() => identity.dispose())

  // Effective identity reported by the hub via UserMaterialized; the
  // active-client gate compares broadcast `active_client_id` against
  // this. Empty until bootstrap; gate treats empty as "unknown — allow"
  // so a sole client plays its ding even before the first heartbeat
  // broadcast settles.
  const [effectiveClientId, setEffectiveClientId] = createSignal('')
  const bumpPending = () => setPendingVersion(v => v + 1)
  // The most recent user id the resume-watermark persistence effect wrote
  // under, so a logout / user switch clears the prior account's watermark.
  let lastResumeUid: string | null = null
  // The op-log/checkpoint persistence policy lives in its own object (see
  // ~/lib/crdt/checkpointRecorder): the queue, the coalesce, the frame counter,
  // the compaction threshold and the rewrite are one unit with one lifetime,
  // which the hook only constructs and disposes. Splitting them across hook
  // scope, a closure and a free function is what produced the two generation-
  // counter bugs the comment below records.
  let recorder: CheckpointRecorder | null = null
  // Hydration cancellation is the reactive owner's job, NOT a hand-rolled
  // counter -- see the `cancelled` flag in the effect below. The counter this
  // replaces shipped two bugs of its own, both from capturing the token at the
  // wrong line: a direct A->B user switch invalidated its OWN hydration (the
  // manager never installed, `hydrated` stayed false, the ready gate never
  // opened the socket), and a threshold-triggered checkpoint write bumped the
  // same counter and cancelled unrelated in-flight hydrations. Solid runs a
  // computation's cleanups both before each re-run and on owner disposal, so
  // one `onCleanup` covers every case the counter enumerated -- INCLUDING
  // disposal, which the counter never covered at all.

  /**
   * Construct a manager for `uid`. One definition so the hydrate path and the
   * hydrate-failure fallback cannot be given different constructor arguments.
   */
  function makeManager(uid: string): PendingOpsManager {
    return new PendingOpsManager(uid, new HLCClock(ownClientId()), bumpPending)
  }

  /**
   * Tear down the current recorder and, once its in-flight IDB write has
   * settled, clear `uid`'s persisted pair. The ordering is the point: both are
   * unawaited writes against the same cached connection, so an append armed in
   * the same turn as a logout could otherwise land after the wipe and leave a
   * previous account's frames on a shared device.
   */
  function disposeRecorderAndClear(uid: string | null): void {
    const dying = recorder
    recorder = null
    const settled = dying ? dying.dispose() : Promise.resolve()
    if (uid)
      void settled.then(() => clearCheckpointAndOpLog(uid))
  }

  /**
   * Deadline on the persisted-checkpoint read. Generous -- this is a
   * last-resort watchdog for a store that has stopped answering at all, not a
   * latency budget; a slow-but-working read must never lose the race and give
   * up a resume it had earned.
   */
  const HYDRATION_TIMEOUT_MS = 10_000

  /**
   * Resolve `promise`, or `null` once `ms` elapses.
   *
   * Deliberately does NOT reject on timeout: every caller here treats a missing
   * payload as "cold-start", so surfacing the deadline as an error would only
   * route it through a catch that does the same thing.
   *
   * `onTimeout` fires when the deadline WINS. It is not decoration: losing the
   * race does not cancel the promise behind it, and the hydration read now has
   * a WRITE half (the sibling seed's adoption). Marking the run superseded is
   * what stops that write landing after the cold start has already rewritten
   * the checkpoint -- see HydrationOptions.superseded.
   */
  function withTimeout<T>(promise: Promise<T>, ms: number, onTimeout?: () => void): Promise<T | null> {
    return new Promise<T | null>((resolve) => {
      const timer = setTimeout(() => {
        log.warn('hydration read did not settle; cold-starting', { ms })
        onTimeout?.()
        resolve(null)
      }, ms)
      promise.then(
        (value) => {
          clearTimeout(timer)
          resolve(value)
        },
        (err) => {
          clearTimeout(timer)
          log.error('hydration read rejected; cold-starting', { err })
          resolve(null)
        },
      )
    })
  }

  /**
   * Touch-interval ticks between abandoned-checkpoint sweeps. At the 60s touch
   * interval this is hourly -- often enough that a long-lived session reclaims
   * within the day, rare enough that an index walk plus destructive deletes
   * stays off any interaction path.
   */
  const SWEEP_EVERY_TICKS = 60

  createEffect(() => {
    const uid = opts.userId()
    // Re-runs when the client id is re-minted after a duplicate-tab collision,
    // so the duplicate re-hydrates under its own owner key instead of sharing
    // the incumbent's checkpoint.
    const clientId = ownClientId()
    // Invalidates this run: Solid fires it before the next execution AND on
    // owner disposal, so an in-flight hydration can never install a manager for
    // a superseded user, a superseded client id, or a torn-down tree.
    let cancelled = false
    // Assigned by the async continuation below, but DECLARED and torn down
    // here. Both must be registered SYNCHRONOUSLY, while this effect is still
    // the ambient Owner: Solid's `runComputation` restores the previous Owner
    // in its `finally`, so an `onCleanup` reached after an `await` sees
    // `Owner === null` and is silently DISCARDED (`solid.js`'s `cleanNode`
    // no-ops on a null owner; only the dev build warns). A discarded clear
    // leaks one interval per effect run, and — because `touchOwner` keeps
    // writing under the OLD owner key — pins that abandoned checkpoint's
    // `lastSeenAt` fresh forever, exempting it from both arms of
    // `sweepAbandonedCheckpoints` and inverting the reclamation this timer
    // exists to make safe.
    let touchTimer: ReturnType<typeof setInterval> | undefined
    onCleanup(() => {
      cancelled = true
      if (touchTimer !== undefined)
        clearInterval(touchTimer)
    })
    if (!uid) {
      setPendingMgr(null)
      setHydrated(false)
      // Best-effort: clear the checkpoint + op-log so the next login starts
      // clean. A stale checkpoint would hydrate correctly (it is keyed and
      // tenant-checked per user), but a logout is the natural "forget this
      // device's state" signal — and clearing spans every tab's rows for that
      // account, not just this one's.
      disposeRecorderAndClear(lastResumeUid)
      lastResumeUid = null
      return
    }
    if (lastResumeUid && lastResumeUid !== uid) {
      disposeRecorderAndClear(lastResumeUid)
      // Drop the OUTGOING account's manager immediately. Without this the
      // stale manager stays published for the whole (async) hydration of the
      // incoming account, so the shell keeps rendering the previous tenant's
      // workspaces and tabs -- and any edit made in that window applies against
      // their state. Only the logout branch above used to clear it.
      setPendingMgr(null)
    }
    lastResumeUid = uid
    setHydrated(false)
    // Hydrate the manager from the persisted checkpoint + op-log BEFORE
    // constructing it, so the WS effect (gated on hydrated) opens against
    // populated confirmedState and the resume cursor resolves to the tight
    // T_now. A null payload (no checkpoint, parse failure, unavailable store)
    // constructs an empty manager — today's cold-start behavior.
    const hydrateAndInstall = async (): Promise<void> => {
      let payload: HydrationPayload | null = null
      // Set when the deadline below WINS. Distinct from `cancelled` (which the
      // effect's own cleanup sets) only in cause; both mean the same thing to
      // the read: this run's result is no longer wanted, so it must not write.
      let deadlineMissed = false
      try {
        // RACED against a deadline, not merely try/caught. Every arm that
        // THROWS or REJECTS already lands on the cold-start path -- but a read
        // that never SETTLES is the one shape a catch cannot see, and this
        // await is the sole thing holding the ready gate shut. `useUserEvents`
        // returns without connecting and without scheduling a reconnect while
        // the gate is closed, so a hung read leaves the user with an empty
        // shell, no CRDT state, no presence, no error UI, and no recovery short
        // of a manual reload.
        //
        // `indexedDB.open` firing none of success/error/blocked is a real
        // browser condition (WebKit IDB hangs, a corrupt profile, a wedged
        // versionchange in another window), and every in-code path is already
        // closed -- so a timeout is the only remaining defence. Losing the race
        // costs one full snapshot, which is exactly what a cold start does
        // anyway.
        //
        // The late read is IGNORED but no longer harmless on its own: seeding
        // from a sibling WRITES (it adopts that sibling's bytes under our own
        // owner key), and an adoption landing after this run has cold-started
        // and begun appending its own op-log ordinals would leave the sibling's
        // older header over our newer log -- a hole whose replayed batchEnd
        // frames advance the resume cursor straight past it. `superseded` is
        // what makes the late read drop its write instead of just its result.
        payload = await withTimeout(
          loadHydrationState(uid, clientId, { superseded: () => cancelled || deadlineMissed }),
          HYDRATION_TIMEOUT_MS,
          () => { deadlineMissed = true },
        )
      }
      catch (err) {
        // loadHydrationState is written to never throw. If it ever does, that
        // is a reason to cold-start, not a reason to leave the app wedged.
        log.error('hydration read failed; cold-starting', { err })
      }
      // This effect re-ran (user switch, client-id re-mint) or its owner was
      // disposed while hydration was in flight; abandon the completion so it
      // cannot install state for a run that is no longer current.
      if (cancelled)
        return
      installRuntimeFor(uid, clientId, payload)
      // Collect checkpoints stranded by tabs that are gone. Their client ids
      // died with their sessionStorage, so nothing else will ever reach those
      // rows -- and each holds a state header plus one chunk per entity.
      // Fire-and-forget and best-effort: this is reclamation, not
      // correctness, and it must not delay the ready gate.
      void sweepAbandonedCheckpoints(uid, clientId)
      // Keep this owner's `lastSeenAt` fresh for as long as the tab runs. That
      // timestamp is the sweep's ONLY liveness signal, and the row is otherwise
      // stamped just once per checkpoint rewrite -- so without this a quiet tab
      // would eventually read as abandoned and have its own checkpoint and
      // op-log collected out from under it.
      //
      // One small put on the metadata row, so it can run on a timer without
      // touching the interaction path. Fire-and-forget: a failed touch only
      // risks a premature collection, which costs one cold start.
      //
      // The `cancelled` guard above already returned for a superseded run, so
      // this only ever arms a timer the synchronous `onCleanup` above will
      // clear.
      void touchOwner(uid, clientId)
      // The sweep rides the SAME timer, every SWEEP_EVERY_TICKS ticks, rather
      // than firing only at hydration. Reclamation keyed to startup scales with
      // session COUNT, and the rows that actually accumulate belong to sessions
      // that never restart: a desktop app or a pinned tab open for weeks strands
      // one owner per sibling tab that closes and never runs a second sweep.
      // Foreign-account rows are the sharpest case -- they get no cap arm at
      // all, only the 14-day TTL, so on a device whose single session never
      // reloads nothing collects them.
      //
      // Every N ticks, not every tick: the sweep walks an index and issues
      // destructive deletes, which is worth doing hourly and not minutely.
      let ticks = 0
      touchTimer = setInterval(() => {
        void touchOwner(uid, clientId)
        if (++ticks % SWEEP_EVERY_TICKS === 0)
          void sweepAbandonedCheckpoints(uid, clientId)
      }, OWNER_TOUCH_INTERVAL_MS)
    }
    // `installRuntimeFor` already lands every failure on the cold-start path,
    // so this exists only so a throw from its own last-resort signal write
    // cannot surface as an unhandled promise rejection.
    void hydrateAndInstall().catch(err => log.error('hydration effect failed', { err }))
  })

  /**
   * Bring the runtime up for `uid`, replaying `payload` when there is one.
   *
   * EVERY failure arm lands on the same cold-start fallback — an empty manager,
   * a recorder, and an OPEN ready gate. That is not defensiveness for its own
   * sake: `useUserEvents` returns before it connects OR schedules a reconnect
   * while the gate is shut, so a throw that escapes this function leaves the
   * user with an empty shell, no error UI, and no recovery short of a manual
   * reload. Cold-starting is the path this module already treats as correct; a
   * dead app is not.
   *
   * "Every path opens the gate" is STRUCTURAL here, not an enumeration: the two
   * cold-start arms are one `coldStart` call, and the last-resort
   * `setHydrated(true)` is the only other opener. The prose used to claim
   * `setHydrated(true)` had exactly one call site, which the last-resort arm
   * already falsified -- the kind of drift a reader cannot check without
   * re-deriving the whole branch tree.
   */
  function installRuntimeFor(uid: string, clientId: string, payload: HydrationPayload | null): void {
    // A cold start under THIS run's owner key. Threading `clientId` rather than
    // re-reading `ownClientId()` inside the install keeps the recorder writing
    // under the id this pass hydrated from: one owner, one source. Reading it
    // live was safe only because the re-mint's signal write happens to run this
    // effect's cleanup synchronously first -- a scheduling property, not an
    // invariant, and not one a future refactor would know it was relying on.
    const coldStart = (): void => installObserverAndManager(makeManager(uid), uid, clientId, undefined)
    try {
      if (payload && hydrateInto(uid, clientId, payload))
        return
      coldStart()
    }
    catch (err) {
      log.error('runtime install failed; degrading to an empty manager', { err })
      try {
        coldStart()
      }
      catch (fatal) {
        // Constructing an empty manager cannot normally fail. If it does, an
        // open gate with a null manager still beats a shut one: useUserEvents
        // null-checks `pending()` on every path and simply refuses payloads
        // until a manager appears, so the socket reconnects and the next
        // effect run can recover.
        log.error('empty-manager fallback failed; opening the ready gate anyway', { err: fatal })
        setHydrated(true)
      }
    }
  }

  /**
   * Replay `payload` into a fresh manager and publish it. Returns false when
   * the persisted pair turned out to be unusable, having already wiped it —
   * the caller then cold-starts.
   */
  function hydrateInto(uid: string, clientId: string, payload: HydrationPayload): boolean {
    const mgr = makeManager(uid)
    try {
      mgr.hydrate(payload)
    }
    catch (err) {
      // A hydrate() failure (e.g. a proto shape mismatch applyOp rejects)
      // leaves the manager in an indeterminate state — fall back to empty by
      // constructing fresh, and wipe the poison pair so the next reload isn't
      // blocked by the same record.
      log.warn('hydrate() failed; falling back to empty state and wiping checkpoint', { err })
      // OWNER-scoped: the poison record belongs to this tab alone, and a
      // user-wide wipe would cold-start every OTHER tab as collateral.
      void clearOwnerCheckpointAndOpLog(uid, clientId)
      return false
    }
    // Hand the recorder the frames hydrate() replayed -- but ONLY when the base
    // they sit on is really on disk under this owner's key.
    //
    // When it is, they seed the frame counter (starting from 0 would make the
    // threshold measure post-hydration appends only, and a run of short
    // sessions that each resume via `delta` grew the persisted log without
    // bound), and they are exactly what moved the state past the persisted
    // chunks, so they seed the DIRTY SET the next incremental rewrite works
    // from.
    //
    // When it is NOT -- a sibling seed whose adoption write failed, or one
    // abandoned because the run was superseded -- the recorder must get NO
    // base, so its first rewrite is FULL. An incremental one would land a
    // header plus a handful of shards with nothing underneath, and the next
    // cold start would hydrate a state silently missing almost every record
    // with the resume cursor riding on it. The state itself is still correct in
    // memory, so this tab still resumes; only its persistence restarts.
    // A promise here is the SEED path deliberately not waiting: its adoption is
    // still committing, and the recorder holds its appends until it lands
    // rather than the whole gate holding for it. Mapped, not awaited -- awaiting
    // would put the wait straight back.
    //
    // Written ONCE, as `toBase`, and applied to whichever arm the union turns
    // out to be: spelling the recorder's base shape separately per arm is how
    // the two would drift on which fields it carries.
    const toBase = (settled: PersistedBase | null): HydratedBase | undefined =>
      settled ? { frames: payload.frames, nextOrdinal: settled.nextOpLogOrdinal } : undefined
    const base = payload.persistedBase
    installObserverAndManager(mgr, uid, clientId, base instanceof Promise ? base.then(toBase) : toBase(base))
    // Compact now when the persisted log was truncated, or is already at/over
    // the threshold. The truncated case is the important one: whatever cut the
    // replay short -- an undecodable frame on our own path, or the source's
    // over-cap tail on the seed path -- leaves the checkpoint pinned further
    // back than the state we just hydrated, so every future reload replays the
    // same prefix and stops at the same place. Rewriting pins a fresh
    // checkpoint at the post-replay watermark and drops the log.
    //
    // On the seed path this necessarily lands while the recorder is still
    // holding for the adoption, so the request is DEFERRED there and re-issued
    // when the base settles -- see the recorder's `heldRewrite`. It used to be
    // refused outright, which meant a seeded tab never performed the one repair
    // this call exists for.
    if (payload.truncated || payload.frames.length >= CHECKPOINT_OP_LOG_THRESHOLD)
      recorder?.rewriteNow()
    return true
  }

  /**
   * Install the confirmed-mutation observer (op-log appender) on the manager,
   * publish it, enable recording, and mark hydration complete so the WS effect
   * opens. Centralized so the hydrate-success and hydrate-fallback paths share
   * the same wiring (the observer + recording gate + hydrated signal).
   *
   * `hydratedFrom` is undefined on every cold-start path, which is what tells
   * the recorder its first checkpoint rewrite must be FULL — nothing usable is
   * persisted for this owner.
   */
  function installObserverAndManager(
    mgr: PendingOpsManager,
    uid: string,
    clientId: string,
    hydratedFrom: CheckpointRecorderOptions['hydratedFrom'],
  ): void {
    // Replace any recorder left over from a previous manager. Nothing is
    // cleared here — this is a re-install for the SAME account, so its
    // persisted pair must survive.
    void recorder?.dispose()
    recorder = createCheckpointRecorder({ userId: uid, clientId, mgr, hydratedFrom })
    const active = recorder
    // Both hooks in ONE call, so recording cannot be enabled without them or
    // vice versa. The checkpoint-reset half matters as much as the append half:
    // a bootstrap wholesale-replaces confirmedState from a fresh hub snapshot,
    // so the persisted checkpoint+op-log MUST re-base to match -- the snapshot
    // IS a perfect checkpoint base (state@maxHlc under its epoch), and any
    // op-log frames accumulated since describe the now-discarded state. Without
    // it, a FALLBACK after hydration (the hub sends `initial` because it could
    // not resume) leaves the persisted pair stale while recording keeps
    // appending, and the next cold reload replays a stale log onto a stale base.
    mgr.attachRecorder({
      record: (frame: WatchUserEvent) => active.record(frame),
      onCheckpointReset: () => active.onCheckpointReset(),
    })
    setPendingMgr(mgr)
    setHydrated(true)
  }

  // A tab holds TWO long-lived sockets against the hub, and one refusal closes
  // whichever asks next -- so the same cap can fire twice for one cause.
  // Collapsing that is the TOAST layer's job (see liveSticky in Toast.tsx), not
  // this hook's: a latch here would never learn that the user dismissed the
  // toast, so the next genuinely-new refusal would be announced nowhere and
  // leave a frozen UI unexplained.
  const announceFatalClose = (info: FatalCloseInfo) => {
    // WHICH terminal close decides what to tell the user: a revoked credential
    // wants a reload, the hub's per-user connection cap wants a tab closed, and
    // giving the second the first's advice sends them round the loop again.
    //
    // Sticky, because nothing recovers this on its own -- neither socket
    // schedules a retry after a terminal close -- so a toast that vanished in
    // three seconds would leave a frozen UI with the explanation already gone.
    showStickyWarnToast(fatalCloseMessage(info))
  }

  // Open the per-user `/ws/userevents` subscription once the user id is
  // known. The hook stays live across workspace switches; per-
  // workspace stores slice the materialized state instead.
  const userEvents = useUserEvents({
    userId: () => opts.userId(),
    activeClient: opts.activeClient,
    pending: () => pendingMgr(),
    // Gate the WS open on hydration so the resume cursor resolves against
    // hydrated confirmedState (the tight T_now), not empty state.
    ready: hydrated,
    onWorkspaceLifecycleChanged: () => opts.onWorkspaceLifecycleChanged(),
    onSubscriberClientId: id => setEffectiveClientId(id),
    onPendingDropped: () => {
    // EntityRemoved dropped at least one pending op (a redacted
    // entity left the visible set with a local mutation still in
    // flight). Surface a warn-toast so the user understands their
    // recent action didn't take effect.
      showWarnToast('A pending change was discarded because the affected item left your view.')
    },
    onFatalClose: announceFatalClose,
  })

  // The channel relay can be refused by the SAME hub code path as the
  // user-events stream, and a tab holds both sockets. Before this it discarded
  // the close entirely, so a capped user got "Failed to open terminal" and two
  // unbounded redial loops kept dialling a connection the hub would refuse
  // every time.
  onCleanup(channelManager.onFatalClose(announceFatalClose))

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
      // Sent over the keepalive transport: a normal fetch started while the
      // page is unloading is cancelled with it, so an op enqueued from a
      // `pagehide` handler would never reach the hub. See CRDTBridge.flushNow.
      flushNow: () => void opsSubmitter.flush({ unload: true }),
      clock: () => mgr.clock,
      originClientId: ownClientId,
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
