import type { MessageInitShape } from '@bufbuild/protobuf'
import type { HLCClock } from './hlc'
import type { HydrationPayload } from './hydrate'
import type { FloatingWindowRecord, HLC, NodeRecord, TabRecord, UserCrdtState, WorkspaceContentsRecord } from '~/generated/leapmux/v1/user_crdt_pb'
import type {
  BatchCommitted,
  BatchRejection,
  CommittedOp,
  CrdtOp,
  EntityMaterialized,
  EntityRemoved,
  OpBatch,
  ResumeDelta,
  WatchUserEvent,
} from '~/generated/leapmux/v1/user_ops_pb'
import { clone, create } from '@bufbuild/protobuf'
import {
  FloatingWindowRecordSchema,
  NodeRecordSchema,
  TabRecordSchema,
  UserCrdtStateSchema,
  WorkspaceContentsRecordSchema,
} from '~/generated/leapmux/v1/user_crdt_pb'
import { BatchRejectionReason, WatchUserEventSchema } from '~/generated/leapmux/v1/user_ops_pb'
import { applyOp, newState } from './apply'
import { entityKey, entityMapFor, TOMBSTONED_CHUNK_KINDS } from './checkpointChunks'
import { hlcClone, hlcCmp, hlcIsZero, hlcMax } from './hlc'
import { opTarget, opTargetKey } from './ops'

/**
 * PendingOpsState captures the local layered view: confirmed (from
 * the hub) plus speculative (confirmed + still-pending optimistic
 * ops). Mutators submit batches; consumers read the projection of
 * speculativeState.
 */
export interface PendingOpsState {
  confirmedState: UserCrdtState
  speculativeState: UserCrdtState
  pendingBatches: OpBatch[]
  /**
   * The epoch the hub most recently reported (bootstrap snapshot, ResumeDelta,
   * or a BatchCommitted echo). The resume watermark is sent under THIS epoch,
   * so the two are one value — there is no separate "resume epoch" that could
   * drift out of lockstep.
   */
  currentEpoch: bigint
  /**
   * The resume cursor: the highest canonical HLC this client is willing to
   * assert it has applied EVERYTHING at or below. useUserEvents sends it on
   * reconnect (alongside currentEpoch) so the hub can ship a delta instead of a
   * full snapshot, and the checkpoint persists it across refreshes.
   *
   * It advances ONLY at a batch boundary — the `batch_end` frame — plus the two
   * whole-state installs that define a boundary by construction (`bootstrap`'s
   * snapshot max, `hydrate`'s checkpoint watermark, and `applyDelta`'s
   * `max_hlc`). Individual remote ops, entity_materialized and entity_removed
   * frames observe their HLC into the clock but deliberately do NOT move this;
   * see the BatchEnd proto doc for why a mid-batch cursor strands the rest of
   * that batch's frames above a strictly-greater watermark forever.
   *
   * Never advanced by speculative local client_hlc from submit(), which the hub
   * does not know about.
   */
  resumeWatermark?: HLC
}

/**
 * Reasons a rejected batch MAY auto-retry without user intervention.
 * Everything else -- including any reason added to the proto later -- is
 * permanent by default, so a newly-introduced server-side rejection can never
 * be silently treated as loop-eligible (an allowlist fails safe where the old
 * denylist failed open: it had already drifted, misclassifying the permanent
 * INVALID_WORKER_REF as retryable). Only EPOCH_REQUIRED is requeued
 * automatically -- after a reconnect refreshes currentEpoch; STALE_EPOCH and
 * every validation/permission denial require the user to re-issue the action.
 */
const RETRYABLE_REJECTIONS = new Set<BatchRejectionReason>([
  BatchRejectionReason.BATCH_REJECTION_EPOCH_REQUIRED,
])

/**
 * Reasons whose rejection means the client's epoch is stale or missing, so a
 * reconnect must refresh `currentEpoch` before any retry can land. Kept beside
 * RETRYABLE_REJECTIONS so the two orthogonal rejection classifications --
 * "may auto-retry" and "needs an epoch refresh first" -- both live in the module
 * that owns rejection classification, instead of the second one drifting as a
 * hardcoded switch in the submitter layer. STALE_EPOCH needs the refresh but is
 * NOT retryable (the user re-issues); EPOCH_REQUIRED needs both.
 */
const EPOCH_REFRESH_REJECTIONS = new Set<BatchRejectionReason>([
  BatchRejectionReason.BATCH_REJECTION_EPOCH_REQUIRED,
  BatchRejectionReason.BATCH_REJECTION_STALE_EPOCH,
])

/**
 * Hard cap on `PendingOpsManager.pendingOwnEchoBatchIds`, the set of batch_ids
 * awaiting their own broadcast echo. A JS Set iterates in insertion order, so
 * the overflow eviction is oldest-first.
 *
 * An echo follows its commit within a round trip, and a reconnect's resume tail
 * re-delivers the ones a socket drop lost, so an id still resident 512 submits
 * later is one whose echo is never coming -- orders of magnitude above what the
 * submitter can have in flight (one SubmitOps per 16ms flush window, a handful
 * of batches each). The cap is what makes the set BOUNDED rather than merely
 * usually-small: bootstrap and hydrate clear it wholesale, but a session that
 * keeps resuming runs neither, so without a cap the residue would grow for the
 * life of the page.
 */
export const MAX_OWN_ECHO_BATCH_IDS = 512

/**
 * A confirmed-mutation observer. Fires per confirmed WatchUserEvent frame
 * applied to confirmedState (live broadcast batch/entity frames, a resume
 * delta's frames, or a synthesized batch frame from a SubmitOps echo). Used by
 * useCrdtRuntime to append each frame to the persisted op-log so a cold reload
 * can replay the post-checkpoint frames and re-reach the tight resume cursor.
 *
 * ONCE per batch_id, not once per frame carrying it: the hub echoes a batch
 * this client submitted back to it, on the live broadcast and again on a resume
 * tail, and consumeBatchCommitted already recorded that batch when the SubmitOps
 * response landed. Those echoes are suppressed (see pendingOwnEchoBatchIds).
 *
 * The frame passed is the SAME WatchUserEvent the hub sent (for wire-origin
 * frames) or a synthesized `batch` frame carrying the canonical-HLC-stamped ops
 * (for consumeBatchCommitted, which has no inbound wire frame). bootstrap is
 * NOT recorded — it is a checkpoint reset, not an incremental op.
 *
 * Recording is on for as long as a recorder is attached (see attachRecorder),
 * and suppressed only while hydrate() replays the
 * op-log through the same consume* handlers and MUST NOT re-record those
 * frames, or the log would grow by N entries on every cold reload.
 */
export type ConfirmedMutationObserver = (frame: WatchUserEvent) => void

/**
 * Optional callback fired when a `bootstrap()` wholesale-replaces confirmedState
 * from a fresh hub snapshot. A bootstrap is a checkpoint RESET — the snapshot's
 * state@maxHlc is a perfect checkpoint base, and any op-log frames accumulated
 * since the last checkpoint describe the now-discarded state. useCrdtRuntime
 * wires this to rewrite the checkpoint (establishing the new base) and truncate
 * the op-log, so the persisted pair can never drift from the live state across
 * a FALLBACK (which arrives as `initial` → bootstrap). Without this, a FALLBACK
 * after hydration leaves the persisted pair stale while recording keeps
 * appending to it, so the next cold reload replays a stale log onto a stale
 * base and re-triggers the FALLBACK in a loop.
 */
export type CheckpointResetObserver = () => void

/**
 * The persistence hooks a checkpoint recorder installs, as ONE value.
 *
 * They are always installed together, torn down together, and come from the
 * same recorder -- so passing them as a pair is what makes "an observer with no
 * recorder" and "a recorder with only half its hooks" unspellable.
 */
export interface RecorderHooks {
  record: ConfirmedMutationObserver
  onCheckpointReset: CheckpointResetObserver
}

/**
 * Whether a confirmed state holds any entity at all.
 *
 * The rule the resume refresh-guard turns on, factored out of
 * `PendingOpsManager.isConfirmedPopulated` so a TEST DOUBLE can call the real
 * predicate instead of hand-copying it. A fake that re-implements the branch
 * under test asserts against its own copy: the production rule could gain a
 * fifth map or invert a condition and every cursor-suppression case would stay
 * green. One definition, two callers, no drift.
 */
export function isStatePopulated(state: {
  workspaces?: Record<string, unknown>
  nodes?: Record<string, unknown>
  tabs?: Record<string, unknown>
  floatingWindows?: Record<string, unknown>
}): boolean {
  return Object.keys(state.workspaces ?? {}).length > 0
    || Object.keys(state.nodes ?? {}).length > 0
    || Object.keys(state.tabs ?? {}).length > 0
    || Object.keys(state.floatingWindows ?? {}).length > 0
}

/**
 * Drop every node / tab / floating-window record whose `tombstoneAt` is at or
 * below `floor`. Mutates `state` in place; returns how many records went.
 *
 * The client mirror of the hub's `PruneTombstonesAtOrBelow`, and it exists
 * because delta-resume removed the only GC the client ever had. `applyOp`
 * installs a tombstone SHELL rather than deleting (so a late op cannot resurrect
 * the record), and `materializedLocked` omits tombstoned records — so every
 * full-snapshot bootstrap used to reset `confirmedState` to a tombstone-free
 * base. A client that keeps resuming never bootstraps, so from that point on
 * every tab, tile and window it ever closed stayed in `confirmedState` forever,
 * and since the checkpoint serializes exactly that state, the persisted blob and
 * the cold-start replay grew without bound. The hub never propagates its own
 * pruning either: `PruneTombstonesAtOrBelow` edits server state and emits no ops.
 *
 * SAFETY mirrors the hub's argument, but needs ONE extra precondition the hub
 * gets for free. Canonical HLCs are monotonic and frames arrive in commit order,
 * so once the resume watermark reaches W every REMOTE frame this client will
 * ever see afterwards carries an HLC strictly above W, and a tombstone at or
 * below W can never be contradicted by one.
 *
 * Local pending ops are not remote frames. `applyOp` lazily creates a missing
 * record, and the tombstone SHELL is the only thing that makes
 * `applySetTabRegister` and friends drop a write — so deleting the shell while a
 * batch targeting that entity is still unconfirmed lets `recomputeSpeculative`
 * replay that batch onto nothing and resurrect the entity as a LIVE record. The
 * hub has no speculative layer, which is why its own PruneTombstonesAtOrBelow
 * needs no such guard.
 *
 * `pinned` is that precondition: entity keys (see `opTargetKey`) whose shells
 * must survive this pass. Callers with unconfirmed local state MUST pass it.
 * Nothing is lost by keeping a shell one round longer — the next compaction
 * collects it once the batch settles.
 */
export function pruneTombstonesAtOrBelow(
  state: UserCrdtState,
  floor: HLC | undefined,
  pinned?: ReadonlySet<string>,
): number {
  if (!floor || hlcIsZero(floor))
    return 0
  let pruned = 0
  // Driven by the SHARDS table and keyed with `entityKey`, rather than a
  // hand-written kind->map list and an inline `${kind}:${id}` template. Those
  // were a third copy of a vocabulary `checkpointChunks` (SHARDS / entityKey)
  // and `ops` (opTarget / opTargetKey) already own, and the copy was the one
  // nothing checked: a new tombstoned entity kind added to the proto is forced
  // into SHARDS by `_everyMapIsSharded`, but a hand-written list here would
  // simply never see it -- and this is the ONLY client-side GC for tombstone
  // shells, so the miss is silent and unbounded.
  for (const kind of TOMBSTONED_CHUNK_KINDS) {
    const map = entityMapFor(state, kind)
    for (const [id, record] of Object.entries(map)) {
      const ts = (record as { tombstoneAt?: HLC }).tombstoneAt
      if (!ts || hlcIsZero(ts))
        continue
      if (pinned?.has(entityKey(kind, id)))
        continue
      if (hlcCmp(ts, floor) <= 0) {
        delete map[id]
        pruned++
      }
    }
  }
  return pruned
}

/**
 * Every entity key any op across `batches` targets, or `null` when some op's
 * body this build does not recognize.
 *
 * `null` means "cannot enumerate", and the only safe response is to prune
 * NOTHING this round -- the caller's contract. Returning a partial set instead
 * would leave the unrecognized op's entity unpinned, and an unpinned shell is
 * exactly what lets `recomputeSpeculative` resurrect a tombstoned record. The
 * mapping itself lives in `opTarget` (ops.ts), shared with the checkpoint's
 * dirty-set so the two cannot disagree about a newly added op kind.
 */
export function pendingEntityKeys(batches: readonly { ops: CrdtOp[] }[]): Set<string> | null {
  const keys = new Set<string>()
  for (const batch of batches) {
    for (const op of batch.ops) {
      const target = opTarget(op)
      if (target.case === 'unknown')
        return null
      const key = opTargetKey(target)
      if (key !== null)
        keys.add(key)
    }
  }
  return keys
}

/** PendingOpsManager is the local CRDT-aware queue. */
export class PendingOpsManager {
  state: PendingOpsState
  /**
   * Optional callback fired after any state-mutating method completes.
   * Bridges Solid reactivity by letting the caller bump a signal so
   * memoized projections re-derive when speculativeState changes. The
   * callback is invoked synchronously on the same tick as the
   * mutation; callers are responsible for batching if needed.
   */
  private readonly notify?: () => void
  /**
   * Optional confirmed-mutation observer. When set AND `recording` is true,
   * each confirmed entry point (consumeRemote / consumeBatchCommitted /
   * consumeEntityMaterialized / consumeEntityRemoved / applyDelta) records its
   * WatchUserEvent frame. See ConfirmedMutationObserver.
   */
  private attached?: RecorderHooks
  /**
   * Suppresses recording during hydrate()'s own op-log replay, so the frames
   * being replayed are not appended back onto the log they came from.
   *
   * PRIVATE, and the only remaining gate. There used to be a public
   * `setRecording` as well, giving four states across two nullable observers
   * and a boolean when only two are meaningful -- `record` had to test
   * `this.recording && this.observeConfirmed` precisely because "observer
   * installed but recording off" and "recording on with no observer" were both
   * spellable. Attaching a recorder now IS enabling recording.
   */
  private replaying = false
  /**
   * batch_ids recorded via consumeBatchCommitted whose wire echo has not yet
   * arrived. The hub broadcasts every committed batch back to ALL subscribers
   * including the originator, so an originating client receives its own batch
   * again through consumeRemote — without this dedup the originator's own batch
   * would be recorded twice (once synthesized by consumeBatchCommitted, once
   * from the wire echo), doubling op-log growth for self-traffic.
   *
   * A SET, not a single slot: one SubmitOps carries every batch the submitter's
   * flush window aggregated, and its response drives one consumeBatchCommitted
   * per batch. A single slot kept only the LAST id, so submitting {A, B} left
   * the slot at B and A's echo re-recorded A — every multi-batch submit
   * double-logged all but its final batch, tripping the checkpoint threshold
   * early and lengthening every cold-reload replay.
   *
   * An id is removed when its echo lands, on EITHER path that can carry it: the
   * live broadcast (consumeRemote) or, when a socket drop lost that broadcast,
   * the reconnect's resume tail (applyFrames' `batch` arm), which re-ships the
   * same batch_id because the hub applies no origin filter.
   *
   * bootstrap and hydrate clear the whole set, but neither is guaranteed to run
   * again -- a client that keeps resuming never bootstraps -- so those are not
   * the bound. MAX_OWN_ECHO_BATCH_IDS is: the residue no echo ever reaches (the
   * hub redacted every op of a self-originated batch, so no batch frame carries
   * the id back) is evicted oldest-first, and an evicted id is merely a batch
   * whose echo would be logged twice. Harmless on replay -- re-applying a batch
   * at the same canonical HLC fails `shouldWrite`'s strict-greater compare --
   * and the op-log is bounded by the checkpoint rewrite either way.
   */
  private readonly pendingOwnEchoBatchIds = new Set<string>()

  constructor(
    // Seeds the pre-bootstrap placeholder state only, and is deliberately NOT
    // retained as a field: bootstrap() replaces confirmedState wholesale with
    // the hub's UserMaterialized, so a kept copy would be a second tenancy key
    // nothing reads and that could only drift from state.userId.
    userId: string,
    public readonly clock: HLCClock,
    notify?: () => void,
  ) {
    // Distinct refs: applying speculative ops must not pollute
    // confirmedState even before bootstrap() arrives.
    this.state = {
      confirmedState: newState(userId),
      speculativeState: newState(userId),
      pendingBatches: [],
      currentEpoch: 1n,
    }
    this.notify = notify
  }

  /**
   * Attach the persistence hooks, or `null` to detach.
   *
   * ONE call rather than three setters. The two hooks are always installed
   * together from the same recorder and always torn down together, and
   * recording is on for exactly as long as one is attached -- so the previous
   * three-knob API could express two states that mean nothing, and the ordering
   * contract between them lived only in the caller's comments.
   */
  attachRecorder(hooks: RecorderHooks | null): void {
    this.attached = hooks ?? undefined
  }

  /** Record a confirmed frame, unless this is hydrate() replaying the log. */
  private record(frame: WatchUserEvent): void {
    if (!this.replaying)
      this.attached?.record(frame)
  }

  /**
   * Wrap one oneof arm in a WatchUserEvent and record it. Every confirmed entry
   * point persists its frame in exactly this shape, so the envelope is built in
   * one place rather than five.
   *
   * Takes the INIT shape, not `WatchUserEvent['event']`: the latter demands
   * fully-constructed messages (`$typeName` and all), while these call sites
   * pass partial initializers like `{ atHlc }`.
   */
  private recordFrame(event: MessageInitShape<typeof WatchUserEventSchema>['event']): void {
    this.record(create(WatchUserEventSchema, { event }))
  }

  /**
   * Advance the resume watermark to the max of itself and `hlc`.
   *
   * Callers are exactly the batch-boundary sites: the `batch_end` frame arm of
   * applyFrames, consumeBatchEnd, and applyDelta's `max_hlc`. (bootstrap and
   * hydrate assign `resumeWatermark` outright rather than coming through here,
   * because they REPLACE confirmedState instead of advancing over it.) Never
   * called with a speculative client_hlc. The watermark is always read
   * alongside currentEpoch (the epoch the hub reported it under), so callers do
   * not pass an epoch here.
   */
  private advanceWatermark(hlc: HLC | undefined): void {
    this.state.resumeWatermark = hlcMax(this.state.resumeWatermark, hlc)
  }

  /**
   * True iff confirmedState holds at least one record in any of its top-level
   * entity maps. The resume refresh-guard uses this to decide whether a delta
   * is safe to fold onto the existing state (a delta is a catch-up over the
   * current visible set, not a full replacement, so folding it onto empty
   * state — a cold start / page refresh — would yield partial state).
   * Authoring the populated check here (next to the state whose shape it
   * describes) keeps the "what counts as populated" knowledge with the schema,
   * so a future top-level map addition is naturally covered by updating one
   * place rather than every caller re-enumerating the maps.
   */
  isConfirmedPopulated(): boolean {
    return isStatePopulated(this.state.confirmedState)
  }

  /** Seed the confirmed + speculative state from a fresh UserMaterialized. */
  bootstrap(materialized: { userId: string, nodes: Record<string, unknown>, tabs: Record<string, unknown>, floatingWindows: Record<string, unknown>, workspaces: Record<string, WorkspaceContentsRecord>, maxHlc?: { physical: bigint, logical: bigint, clientId: string }, currentEpoch: bigint }): void {
    const confirmed = create(UserCrdtStateSchema, {
      userId: materialized.userId,
      nodes: materialized.nodes as never,
      tabs: materialized.tabs as never,
      floatingWindows: materialized.floatingWindows as never,
      workspaces: materialized.workspaces as never,
      maxHlc: hlcClone(materialized.maxHlc as never),
    })
    this.state.confirmedState = confirmed
    // currentEpoch has ONE authoritative home on PendingOpsState: it is read by
    // useOpsSubmitter (SubmitOps) and useUserEvents (the resume cursor). The
    // confirmedState.currentEpoch proto field is NOT mirrored here, because
    // applyDelta / consumeBatchCommitted advance ONLY this.state.currentEpoch
    // and a mirrored copy would silently drift until the next bootstrap.
    this.state.currentEpoch = materialized.currentEpoch
    // A full bootstrap RESETS the watermark: confirmedState is now exactly at
    // the snapshot's max_hlc under its epoch, so that is the new resume point
    // (overwriting any stale pre-reconnect value). This also covers the FALLBACK
    // path: when the hub could not resume, it sent `initial`, and the client's
    // watermark is re-seeded here from the fresh snapshot.
    this.state.resumeWatermark = hlcClone(materialized.maxHlc as never)
    // A bootstrap discards every pre-existing confirmed-state lineage: clear the
    // own-batch dedup key so a freshly-echoed batch isn't suppressed by an id
    // carried over from before the reset, and clear any pending batches (the
    // fresh snapshot supersedes speculative edits in flight against the discarded
    // state — the hub's authoritative view is now confirmedState).
    this.pendingOwnEchoBatchIds.clear()
    this.state.pendingBatches = []
    this.recomputeSpeculative()
    this.clock.observe(materialized.maxHlc as never)
    this.notify?.()
    // The snapshot IS a perfect checkpoint base (state@maxHlc under its epoch),
    // so fire the reset observer: useCrdtRuntime rewrites the checkpoint +
    // truncates the op-log so the persisted pair matches the live state. Without
    // this, a FALLBACK after hydration (the hub sends `initial` because it could
    // not resume the hydrated cursor) leaves the persisted pair stale while
    // recording keeps appending, and the next cold reload replays a stale log
    // onto a stale base, re-triggering the FALLBACK in a loop.
    this.attached?.onCheckpointReset()
  }

  /**
   * Hydrate confirmedState from a persisted checkpoint + op-log on a cold
   * reload. Unlike bootstrap() (which RESETS the watermark from a fresh hub
   * snapshot), hydrate pins the watermark to the checkpoint's `T_c` and then
   * replays the op-log frames through the SAME consume* handlers a live
   * broadcast uses — so confirmedState advances from T_c to T_now and the
   * watermark advances to T_now, letting the client present the TIGHT resume
   * cursor and receive a minimal delta. Recording is disabled during replay
   * so the replayed frames are not re-appended to the op-log.
   *
   * Pure: takes already-parsed objects (no IDB, no proto codecs), so the
   * hydration policy (wipe-both-on-parse-failure) lives in hydrate.ts and this
   * method is unit-testable in isolation.
   */
  hydrate(payload: HydrationPayload): void {
    // Install the checkpoint base. The state already carries its own maxHlc
    // (serialized in the blob), so no re-derive is needed — observe it into
    // the clock so the next Tick is strictly greater.
    this.state.confirmedState = payload.state
    this.state.currentEpoch = payload.currentEpoch
    this.state.resumeWatermark = hlcClone(payload.watermark)
    if (payload.state.maxHlc)
      this.clock.observe(payload.state.maxHlc)
    // A fresh hydrate clears the pending set + own-batch dedup key: the
    // checkpoint+op-log lineage is authoritative, and any in-flight speculative
    // edits from a prior (pre-reload) session died with the old JS heap.
    this.state.pendingBatches = []
    this.pendingOwnEchoBatchIds.clear()

    // Replay the op-log through the SAME frame dispatch a ResumeDelta uses,
    // WITHOUT recording (these frames are already persisted; re-recording would
    // duplicate them on the next cold reload).
    const wasReplaying = this.replaying
    this.replaying = true
    try {
      // droppedPending is structurally always false here: pendingBatches was
      // just cleared above, so dropPendingByPredicate has nothing to drop.
      this.applyFrames(payload.frames)
    }
    finally {
      this.replaying = wasReplaying
    }
    this.recomputeSpeculative()
    this.notify?.()
  }

  /**
   * Apply an ordered WatchUserEvent stream to confirmedState, recording each
   * frame (subject to the replay suppression and the own-echo dedup below). The
   * SINGLE dispatch shared by the cold-reload op-log replay (`hydrate`) and the
   * resume tail (`applyDelta`), so the two cannot drift on a newly-added frame
   * arm — they previously kept two hand-synced copies of this switch.
   *
   * Returns whether any pending batch was dropped by an `entityRemoved` frame,
   * which the caller surfaces as a user-visible "pending edit dropped" notice.
   */
  private applyFrames(frames: readonly WatchUserEvent[]): { droppedPending: boolean } {
    let droppedPending = false
    for (const frame of frames) {
      // Whether this frame still needs appending to the op-log. Only the `batch`
      // arm can answer no -- see below.
      let unlogged = true
      switch (frame.event.case) {
        case 'entityMaterialized':
          this.applyMaterializedCore(frame.event.value)
          break
        case 'batch':
          this.applyRemoteBatch(frame.event.value)
          // The SAME own-echo dedup consumeRemote applies, because the hub
          // applies no origin filter to a resume tail either: a batch this
          // client submitted and consumeBatchCommitted already logged comes
          // back down the tail whenever the live broadcast was lost to the
          // socket drop that caused the resume. Recording it again would put
          // two frames for one batch_id in the op-log.
          unlogged = !this.pendingOwnEchoBatchIds.delete(frame.event.value.batchId)
          break
        case 'entityRemoved':
          if (this.applyRemovedCore(frame.event.value).droppedPending)
            droppedPending = true
          break
        case 'batchEnd':
          this.applyBatchEndCore(frame.event.value.atHlc)
          break
        default:
          // Empty / unknown / bootstrap-only arms (initial, delta, presence):
          // skip. Do not advance the watermark on a frame that installed
          // nothing.
          break
      }
      // Record each applied frame — a resume delta's frames advanced
      // confirmedState, so a cold reload must replay them too. hydrate()
      // disables recording during ITS replay so these aren't double-logged.
      if (unlogged)
        this.record(frame)
    }
    return { droppedPending }
  }

  /**
   * Fold a remote (or echoed) batch into confirmedState: observe + apply each
   * op's canonical HLC, and drop any locally-pending batch with the same id
   * (the canonical echo replaces the
   * speculative client_hlc). Shared by `applyDelta` (a ResumeDelta's tail) and
   * `consumeRemote` (a live broadcast) so the two catch-up paths cannot drift
   * on the observe/apply rule — they previously hand-duplicated this loop and
   * had already diverged on whether the watermark advanced per-op.
   *
   * It does NOT advance the resume watermark; `batch_end` is the sole advance
   * point (see advanceWatermark). Every frame of a batch carries the same
   * at_hlc, so advancing here would jump the cursor to the batch max on its
   * FIRST frame, and a socket drop mid-sequence would strand the rest above a
   * strictly-greater cursor the hub never re-ships -- now persisted into the
   * checkpoint, so it would survive refreshes too.
   */
  private applyRemoteBatch(batch: OpBatch): void {
    const idx = this.state.pendingBatches.findIndex(b => b.batchId === batch.batchId)
    for (const op of batch.ops) {
      this.clock.observe(op.canonicalHlc)
      applyOp(this.state.confirmedState, op)
    }
    // No watermark advance here either: an entity_removed frame for the same
    // batch may still follow, and losing it would leave a redacted entity
    // rendered with the cursor already past the batch. `batchEnd` closes it.
    if (idx >= 0)
      this.state.pendingBatches.splice(idx, 1)
  }

  /**
   * Apply a ResumeDelta: the ordered WatchUserEvent frame stream the hub
   * shipped instead of a full snapshot on a successful resume. Frames use the
   * SAME envelope live Send uses (entityMaterialized | batch | entityRemoved),
   * so this dispatches each through the SAME handler a live broadcast uses
   * (applyMaterializedCore / applyRemoteBatch / applyRemovedCore), WITHOUT the
   * wholesale confirmedState replace bootstrap performs, and WITHOUT disturbing
   * pendingBatches beyond dropping any batch whose id the tail echoes back.
   * Observes max_hlc into the clock AND advances the resume watermark to it.
   *
   * Note: a repeated batch frame is RE-APPLIED, not skipped — re-application is
   * a no-op because `shouldWrite` is a strict-greater LWW compare on the same
   * canonical HLC. The batch_id dedup on this path governs RECORDING only: a
   * self-originated batch consumeBatchCommitted already appended to the op-log
   * is not appended a second time when the tail re-ships it.
   */
  applyDelta(delta: ResumeDelta): { droppedPending: boolean } {
    const { droppedPending } = this.applyFrames(delta.frames)
    this.state.currentEpoch = delta.currentEpoch
    this.clock.observe(delta.maxHlc)
    this.advanceWatermark(delta.maxHlc)
    this.recomputeSpeculative()
    this.notify?.()
    return { droppedPending }
  }

  /** Push a fresh local batch and apply it speculatively. */
  submit(batch: OpBatch): void {
    // Detach every record THIS batch will touch, always — the clone is
    // per-batch, not per-manager. `apply.ts` writes registers in place, and
    // `cloneStateForBatches` deep-clones only the records the batches it was
    // GIVEN name, shallow-copying the maps around them. So once a batch is
    // pending, speculativeState still shares every record that batch left
    // alone with confirmedState, and a second batch touching one of those
    // would write straight through into confirmed state. That write is not
    // merely visible early: recomputeSpeculative re-derives FROM
    // confirmedState, so a later reject cannot undo it, and it lands under
    // the local clientHlc, which then suppresses lower-HLC remote writes.
    //
    // Cloning out of speculativeState (not confirmedState) is what makes one
    // unconditional call cover both cases: when the states are aliased it
    // reads confirmed, and when they are already detached it preserves the
    // earlier batches' speculative writes. The cost is one shallow copy of
    // the four maps per submit — the same O(records) pointer work
    // recomputeSpeculative already pays on every commit echo.
    this.state.speculativeState = cloneStateForBatches(this.state.speculativeState, [batch])
    this.state.pendingBatches.push(batch)
    for (const op of batch.ops)
      applySpeculative(this.state.speculativeState, op)
    this.notify?.()
  }

  /**
   * Apply a remote batch (or our own echo). When the incoming batch's
   * id matches a locally-pending batch, this is an echo of our own
   * submission: drop the pending batch and apply the canonical ops to
   * confirmedState. Otherwise, apply each op fresh.
   *
   * Note: with per-subscriber visibility filtering, the wire batch may
   * contain only a subset of the original ops (workspace-redacted ones
   * are stripped). This DOES happen for our own echo: an entity the batch
   * CREATES has no pre-state, so `IsAllowed("")` is false for every filter
   * including the originator's, and the hub sends it as `EntityMaterialized`
   * instead of as a batch op. We apply whatever arrives and drop the pending
   * batch; the stripped ops reach us through the materialized frame, which
   * the hub emits BEFORE this one precisely so the result is consistent.
   */
  consumeRemote(batch: OpBatch): void {
    this.applyRemoteBatch(batch)
    this.recomputeSpeculative()
    // The hub broadcasts every committed batch to ALL subscribers including the
    // originator, so an originating client receives its own batch again here
    // AFTER consumeBatchCommitted already recorded it (the SubmitOps echo fires
    // before the WS broadcast). Skip the duplicate recording: the op-log already
    // holds a frame for this batchId with the canonical-HLC-stamped ops. A
    // genuine remote batch (different batchId) records normally. Consume the
    // id so a later batch reusing it (impossible in practice) is not swallowed.
    if (!this.pendingOwnEchoBatchIds.delete(batch.batchId))
      this.recordFrame({ case: 'batch', value: batch })
    this.notify?.()
  }

  /**
   * The batch-boundary rule, shared by the live path (`consumeBatchEnd`) and
   * the replay/resume path (`applyFrames`' `batchEnd` arm) — the last frame
   * kind that still had two hand-written implementations, and they had ALREADY
   * drifted: `applyFrames` observed and advanced unguarded while
   * `consumeBatchEnd` refused a missing `at_hlc`, so a malformed frame moved
   * the cursor on one path and not the other. Its `*Core` siblings
   * (`applyMaterializedCore`, `applyRemovedCore`, `applyRemoteBatch`) already
   * work this way.
   *
   * This is the ONLY watermark-advance point. Every frame of a batch carries
   * the same `at_hlc`, so advancing per frame moved the cursor to the batch's
   * max on its FIRST frame; a drop before the rest of the sequence then
   * stranded those ops above the cursor permanently. Advancing only at the
   * boundary means a mid-sequence drop re-ships the whole batch, which is
   * idempotent under strict-greater LWW.
   *
   * Returns whether the boundary was applied, so a caller can skip work that
   * only makes sense for a real boundary.
   */
  private applyBatchEndCore(atHlc: HLC | undefined): boolean {
    if (!atHlc)
      return false
    this.clock.observe(atHlc)
    this.advanceWatermark(atHlc)
    return true
  }

  /**
   * Apply a live `batch_end` frame: the boundary at which a batch's catch-up
   * sequence is complete and the resume cursor may move. See the BatchEnd proto
   * doc for why this is the only advance point.
   */
  consumeBatchEnd(atHlc: HLC | undefined): void {
    if (!this.applyBatchEndCore(atHlc))
      return
    // record() must stay: a cold reload replays the op-log through these same
    // handlers, and without the batch_end frame the replayed watermark would
    // stop at the checkpoint.
    this.recordFrame({ case: 'batchEnd', value: { atHlc } })
    // No notify(): this mutates only the clock and the resume watermark, and
    // project() reads neither. crdtState is declared `{ equals: false }`, so a
    // tick here would re-run the whole projection traversal and hand back an
    // identical Projection -- doubling the settled-tick cost of every remote
    // batch (consumeRemote already ticked) for no observable change.
  }

  /** Apply a BatchCommitted: replace pending batch's HLCs with canonical and apply to confirmed. */
  consumeBatchCommitted(batchId: string, committed: BatchCommitted): void {
    const idx = this.state.pendingBatches.findIndex(b => b.batchId === batchId)
    // Refresh the epoch even when this client has no pending batch with the id
    // (the batch was reverted client-side after an EPOCH_REQUIRED give-up, or
    // never originated here): the hub's epoch is authoritative, and skipping it
    // would leave currentEpoch stale so the next SubmitOps gets STALE_EPOCH and
    // re-triggers a reconnect cycle.
    //
    // Monotonic, because this arrives over the Connect RPC while the epoch also
    // advances over the WS, and the two transports have no ordering between
    // them. useOpsSubmitter holds no in-flight guard (flush clears its timer
    // before awaiting, so an enqueue mid-await starts a second RPC), so a
    // response minted under the OLD epoch can land after a bootstrap already
    // installed the new one. Assigning unconditionally would regress the epoch,
    // and decideResume requires an EXACT match -- so the next reconnect would
    // fail the resume gate and pay the full projection snapshot this branch
    // exists to avoid.
    if (committed.epoch > this.state.currentEpoch)
      this.state.currentEpoch = committed.epoch
    if (idx < 0) {
      this.notify?.()
      return
    }
    const batch = this.state.pendingBatches[idx]
    const byOpId = new Map<string, CommittedOp>()
    for (const c of committed.committed) byOpId.set(c.opId, c)
    for (const op of batch.ops) {
      const c = byOpId.get(op.opId)
      if (!c)
        continue
      op.canonicalHlc = c.canonicalHlc
      this.clock.observe(c.canonicalHlc)
      applyOp(this.state.confirmedState, op)
      // Deliberately NO advanceWatermark here. This is the SubmitOps echo,
      // which arrives over the Connect RPC -- a transport entirely separate
      // from the WS the hub's frames ride. Advancing per committed op moved the
      // cursor on THIS batch's canonical HLC while a peer's earlier batch could
      // still be sitting unread in the socket buffer; if the socket then
      // dropped, the reconnect presented a cursor above ops it had never
      // applied, ListUserOpBatchesAfter is strictly-greater so they were never
      // re-sent, and since the checkpoint landed the bad cursor survived
      // refreshes too. The hub does NOT suppress self-echo -- it broadcasts
      // this batch and its batch_end back to the originator like any other
      // subscriber -- so the cursor still advances, just at the batch boundary
      // where it is safe. A cursor that lags only costs idempotent
      // re-delivery, which is exactly the trade the BatchEnd proto doc names.
    }
    // Record the canonical-HLC-stamped batch as a synthesized `batch` frame.
    // consumeBatchCommitted has no inbound wire frame (it fires from the
    // SubmitOps echo), but the ops just landed on confirmedState, so a cold
    // reload must replay them. The batch's ops now carry canonicalHlc, which is
    // exactly what applyRemoteBatch reads on replay. The observer serializes
    // the frame to bytes immediately (synchronously, before splice), so sharing
    // the ops reference is safe — and after splice nothing else mutates these
    // op objects.
    //
    // Mark the id so the hub's echo of THIS batch -- the live broadcast via
    // consumeRemote, or the resume tail via applyFrames when a drop lost that
    // broadcast -- is not recorded a second time. The echo carries the same
    // batchId and would otherwise double every self-originated batch in the
    // op-log. A Set, so a multi-batch SubmitOps marks EVERY one of its batches
    // rather than only the last.
    this.pendingOwnEchoBatchIds.add(batch.batchId)
    // Evict oldest-first past the cap, so the set stays bounded even in a
    // session that resumes forever and therefore never clears it wholesale.
    // Deleting the entry the for-of is standing on is well-defined for a Set:
    // already-visited entries do not affect the remaining iteration.
    for (const oldest of this.pendingOwnEchoBatchIds) {
      if (this.pendingOwnEchoBatchIds.size <= MAX_OWN_ECHO_BATCH_IDS)
        break
      this.pendingOwnEchoBatchIds.delete(oldest)
    }
    this.recordFrame({ case: 'batch', value: { batchId: batch.batchId, ops: batch.ops } })
    this.state.pendingBatches.splice(idx, 1)
    this.recomputeSpeculative()
    this.notify?.()
  }

  /**
   * Apply a BatchRejection.
   *
   * A RETRYABLE rejection (EPOCH_REQUIRED) is NOT terminal -- the submitter
   * requeues the batch -- so its optimistic ops are KEPT in the pending list and
   * stay applied to speculativeState, and the retry's BatchCommitted reconciles
   * it just like any in-flight batch.
   *
   * That keeps the edit visible across the reconnect+retry window ONLY when the
   * reconnect resumes. EPOCH_REQUIRED is the one retryable reason, and an epoch
   * bump fails `decideResume`'s epoch-equality test, so the hub always answers
   * with a full snapshot -- whose `bootstrap()` clears `pendingBatches`
   * outright, since the snapshot is the hub's authoritative view and supersedes
   * speculative edits made against the state it replaced. On that path the edit
   * DOES visibly revert and re-apply, and `revertPendingBatch` becomes a no-op
   * because there is no longer a pending entry to revert.
   *
   * No ops are lost either way: the submitter still holds the batch in its own
   * queue, and `applyRemoteBatch` applies the hub's echo unconditionally, before
   * the pending-index lookup. What is lost is the overlay, and with it the
   * flicker-free property -- so this path is a UI cost, not a correctness one.
   * See the matching note in `useOpsSubmitter.rescheduleForWireRetry`.
   *
   * A non-retryable rejection (a permanent denial,
   * STALE_EPOCH, or a transport give-up passing reason 0) IS terminal, so the
   * batch is dropped and its optimistic ops reverted. If the submitter later
   * gives up on a kept retryable batch (retry cap, or no reconnect handler to
   * refresh the epoch), it calls revertPendingBatch to drop it then.
   */
  consumeBatchRejected(batchId: string, rejection: BatchRejection): { reason: number, offendingOpId: string, retryable: boolean, needsEpochRefresh: boolean } {
    const retryable = RETRYABLE_REJECTIONS.has(rejection.reason)
    if (!retryable)
      this.revertPendingBatch(batchId)
    return {
      reason: rejection.reason,
      offendingOpId: rejection.offendingOpId,
      retryable,
      needsEpochRefresh: EPOCH_REFRESH_REJECTIONS.has(rejection.reason),
    }
  }

  /**
   * Drop a pending batch and revert its optimistic ops from speculativeState.
   * Used for a terminal rejection and when the submitter finally gives up on a
   * retryable batch whose optimistic ops it had kept applied.
   */
  revertPendingBatch(batchId: string): void {
    const idx = this.state.pendingBatches.findIndex(b => b.batchId === batchId)
    if (idx < 0)
      return
    this.state.pendingBatches.splice(idx, 1)
    this.recomputeSpeculative()
    this.notify?.()
  }

  /**
   * Apply an EntityMaterialized: install the full record into
   * confirmedState's matching map slot. The hub sends this when an
   * entity ENTERS the subscriber's allowed set as a side effect of a
   * workspace move; raw move ops are suppressed for becoming-visible
   * subscribers (they would carry pre-state from a hidden workspace),
   * so this event is the only way a fresh entity arrives on this
   * client.
   */
  consumeEntityMaterialized(evt: EntityMaterialized): void {
    this.applyMaterializedCore(evt)
    this.recomputeSpeculative()
    this.recordFrame({ case: 'entityMaterialized', value: evt })
    this.notify?.()
  }

  /**
   * applyMaterializedCore is the state-mutation half of
   * consumeEntityMaterialized (observe atHlc + install the record),
   * WITHOUT recomputeSpeculative/notify. applyDelta calls it per frame
   * so a delta with many materialized frames does one recompute/notify
   * at the end instead of one per frame. Mirrors applyRemoteBatch's
   * "core only" split from consumeRemote.
   */
  private applyMaterializedCore(evt: EntityMaterialized): void {
    // Observe the HLC into the clock, but do NOT advance the resume watermark:
    // that happens only on the batch's `batchEnd` frame. Every frame of one
    // batch carries the SAME at_hlc (the batch's last-op HLC), so advancing
    // here would jump the cursor to the batch's max on its FIRST frame -- and a
    // socket drop before the rest of the sequence would strand the remaining
    // ops above the cursor forever (the resume scan is strictly-greater).
    if (evt.atHlc)
      this.clock.observe(evt.atHlc)
    const entity = evt.entity
    switch (entity.case) {
      case 'tab': {
        const t = entity.value as TabRecord
        this.state.confirmedState.tabs[t.tabId] = t
        break
      }
      case 'floatingWindow': {
        const fw = entity.value as FloatingWindowRecord
        this.state.confirmedState.floatingWindows[fw.windowId] = fw
        break
      }
      case 'node': {
        const n = entity.value as NodeRecord
        this.state.confirmedState.nodes[n.nodeId] = n
        break
      }
      default:
        // Empty / unknown entity oneof. Nothing was installed into
        // confirmedState; fall through to return. The resume cursor is not
        // stalled by dropping this frame because it does not move here at all:
        // the batch's own `batch_end` is what advances it, and that arrives
        // whether or not this arm was understood.
        break
    }
  }

  /**
   * Apply an EntityRemoved: delete the entity from confirmedState
   * AND drop any pending ops touching that entity (otherwise a
   * pending mutation would resurrect a redacted entity from partial
   * state). EntityRemoved is NOT a CRDT tombstone — it's a view-state
   * notification triggered by a workspace move that pushed the entity
   * out of the subscriber's allowed set.
   *
   * Returns whether any pending ops were dropped so the caller can
   * surface a warn-toast when the dropped op was active-tab-related.
   */
  consumeEntityRemoved(evt: EntityRemoved): { droppedPending: boolean } {
    const result = this.applyRemovedCore(evt)
    this.recomputeSpeculative()
    this.recordFrame({ case: 'entityRemoved', value: evt })
    this.notify?.()
    return result
  }

  /**
   * applyRemovedCore is the state-mutation half of consumeEntityRemoved
   * (observe atHlc + delete + dropPendingByPredicate), WITHOUT
   * recomputeSpeculative/notify. applyDelta calls it per frame so a
   * delta with many removed frames does one recompute/notify at the end
   * and aggregates the droppedPending flag across them. Mirrors
   * applyMaterializedCore's split.
   */
  private applyRemovedCore(evt: EntityRemoved): { droppedPending: boolean } {
    let droppedPending = false
    // Observe only; the watermark advances on `batchEnd`. See
    // applyMaterializedCore for why per-frame advancing is unsafe.
    if (evt.atHlc)
      this.clock.observe(evt.atHlc)
    const entity = evt.entity
    switch (entity.case) {
      case 'tab': {
        const ident = entity.value
        delete this.state.confirmedState.tabs[ident.tabId]
        droppedPending = this.dropPendingByPredicate((op) => {
          const body = op.body
          if (body.case === 'setTabRegister' || body.case === 'tombstoneTab')
            return body.value.tabId === ident.tabId
          return false
        })
        break
      }
      case 'windowId': {
        const id = entity.value
        delete this.state.confirmedState.floatingWindows[id]
        droppedPending = this.dropPendingByPredicate((op) => {
          const body = op.body
          if (body.case === 'setFloatingWindowRegister' || body.case === 'tombstoneFloatingWindow')
            return body.value.windowId === id
          return false
        })
        break
      }
      case 'nodeId': {
        const id = entity.value
        delete this.state.confirmedState.nodes[id]
        droppedPending = this.dropPendingByPredicate((op) => {
          const body = op.body
          if (body.case === 'setNodeRegister' || body.case === 'tombstoneNode')
            return body.value.nodeId === id
          return false
        })
        break
      }
      default:
        // Empty / unknown entity oneof: nothing was evicted and no pending op
        // was dropped. As in applyMaterializedCore, the resume cursor is
        // unaffected -- it advances on the batch's `batch_end`, not here.
        return { droppedPending: false }
    }
    return { droppedPending }
  }

  /** dropPendingByPredicate removes every op for which `pred` returns true and returns whether any ops were dropped. */
  private dropPendingByPredicate(pred: (op: CrdtOp) => boolean): boolean {
    let dropped = false
    for (const batch of this.state.pendingBatches) {
      const before = batch.ops.length
      batch.ops = batch.ops.filter(op => !pred(op))
      if (batch.ops.length !== before)
        dropped = true
    }
    this.state.pendingBatches = this.state.pendingBatches.filter(b => b.ops.length > 0)
    return dropped
  }

  /**
   * Re-fold every pending batch on top of confirmedState. Public so
   * the caller (useUserEvents) can flush after directly mutating
   * confirmedState in response to EntityMaterialized / EntityRemoved
   * events.
   *
   * Fast path: when no batches are pending, speculativeState aliases
   * confirmedState — we skip the clone-and-replay since they're
   * guaranteed equal. `submit` detaches the alias before its first
   * mutation so the alias never escapes as a writable reference.
   */
  /**
   * Drop tombstone shells the resume cursor has already moved past, then
   * re-derive speculativeState. Returns how many records went.
   *
   * The client's half of what `Manager.maybeCompact` does server-side: prune
   * tombstones and rewrite the checkpoint together. It runs from the checkpoint
   * rewrite for exactly that reason -- pruning is only worth anything if the
   * smaller state is what gets persisted, and pairing them means the two can
   * never disagree about which shells the blob still carries. See
   * `pruneTombstonesAtOrBelow` for why the resume watermark is a safe floor.
   *
   * Shells of entities an unconfirmed local batch still targets are PINNED: this
   * method re-derives speculativeState immediately below, and that replay would
   * otherwise resurrect the pruned entity as a live record (`applyOp` lazily
   * creates what it cannot find, and the shell is the only thing that makes the
   * register writes drop).
   */
  compactTombstones(): number {
    const pinned = pendingEntityKeys(this.state.pendingBatches)
    if (pinned === null) {
      // A pending op this build cannot map to an entity. Pruning now could drop
      // the very shell that op's replay needs to be suppressed by, so skip the
      // whole pass -- shells cost bytes, a resurrected record costs correctness.
      // The next compaction retries once the batch settles.
      return 0
    }
    const pruned = pruneTombstonesAtOrBelow(
      this.state.confirmedState,
      this.state.resumeWatermark,
      pinned,
    )
    if (pruned > 0) {
      this.recomputeSpeculative()
      this.notify?.()
    }
    return pruned
  }

  recomputeSpeculative(): void {
    if (this.state.pendingBatches.length === 0) {
      this.state.speculativeState = this.state.confirmedState
      return
    }
    const cloned = cloneStateForBatches(this.state.confirmedState, this.state.pendingBatches)
    for (const batch of this.state.pendingBatches) {
      for (const op of batch.ops)
        applySpeculative(cloned, op)
    }
    this.state.speculativeState = cloned
  }
}

/**
 * applySpeculative wraps applyOp with the speculative HLC selection
 * shared by both submit() and recomputeSpeculative(): prefer the
 * canonical HLC (assigned by the hub on commit) when present,
 * otherwise fall back to the local client_hlc as a per-apply override.
 * applySpeculative never mutates the op — it passes the client_hlc as a
 * per-apply override rather than writing op.canonicalHlc — so wire-emit
 * reads the same batch object later and the hub still rejects ops that
 * arrive with canonical_hlc pre-set. (canonicalHlc IS written, but only
 * by consumeBatchCommitted on commit, after which the op is never re-sent.)
 */
function applySpeculative(state: UserCrdtState, op: CrdtOp): void {
  applyOp(state, op, op.canonicalHlc ? undefined : (op.clientHlc ?? undefined))
}

/**
 * cloneStateForBatches returns a state where every record the
 * `batches` will mutate is deep-cloned, and every other record is
 * shared by reference with `state`. Top-level maps are always shallow-
 * copied so that creating new records via apply (e.g. lazy-ensure or
 * tombstone-replace) lands in the cloned map without leaking into
 * `state`.
 *
 * apply.ts mutates a record in place for `set*Register` ops, but
 * tombstone ops REPLACE the map slot with a fresh record — those
 * don't need pre-cloning. Similarly setWorkspaceRootNode mutates the
 * workspace record in place, so we pre-clone its slot when present.
 * setWorkspaceRegister only creates a record when absent (never mutates
 * an existing one) and tombstoneWorkspace `delete`s the slot — both land
 * in the shallow-copied `workspaces` map without touching the shared
 * record, so neither needs a pre-clone (same reasoning as tombstone ops).
 *
 * Mirrors the backend's `CloneStateForBatch` (state.go).
 */
function cloneStateForBatches(state: UserCrdtState, batches: OpBatch[]): UserCrdtState {
  const nodes: Record<string, NodeRecord> = { ...state.nodes }
  const tabs: Record<string, TabRecord> = { ...state.tabs }
  const floatingWindows: Record<string, FloatingWindowRecord> = { ...state.floatingWindows }
  const workspaces = { ...state.workspaces }

  const clonedNodes = new Set<string>()
  const clonedTabs = new Set<string>()
  const clonedFws = new Set<string>()
  const clonedWss = new Set<string>()
  for (const batch of batches) {
    for (const op of batch.ops) {
      const body = op.body
      switch (body.case) {
        case 'setNodeRegister': {
          const id = body.value.nodeId
          if (!clonedNodes.has(id) && nodes[id]) {
            nodes[id] = clone(NodeRecordSchema, nodes[id])
            clonedNodes.add(id)
          }
          break
        }
        case 'setTabRegister': {
          const id = body.value.tabId
          if (!clonedTabs.has(id) && tabs[id]) {
            tabs[id] = clone(TabRecordSchema, tabs[id])
            clonedTabs.add(id)
          }
          break
        }
        case 'setFloatingWindowRegister': {
          const id = body.value.windowId
          if (!clonedFws.has(id) && floatingWindows[id]) {
            floatingWindows[id] = clone(FloatingWindowRecordSchema, floatingWindows[id])
            clonedFws.add(id)
          }
          break
        }
        case 'setWorkspaceRootNode': {
          const id = body.value.workspaceId
          // SetWorkspaceRootNode is set-once via applyOp; if rootNodeId
          // is already non-empty the op is a no-op and cloning the
          // record would be wasted work. Only clone when the slot is
          // empty or the workspace record is yet to be materialized.
          if (!clonedWss.has(id) && workspaces[id] && workspaces[id].rootNodeId === '') {
            workspaces[id] = clone(WorkspaceContentsRecordSchema, workspaces[id])
            clonedWss.add(id)
          }
          break
        }
        // Tombstone ops replace the map slot with a fresh record;
        // they do not mutate the pre-existing record, so no pre-clone
        // is needed.
      }
    }
  }

  return create(UserCrdtStateSchema, {
    userId: state.userId,
    nodes,
    tabs,
    floatingWindows,
    workspaces,
    maxHlc: hlcClone(state.maxHlc),
    compactionWatermark: hlcClone(state.compactionWatermark),
    opRetentionWatermark: hlcClone(state.opRetentionWatermark),
    currentEpoch: state.currentEpoch,
    epochStartedAt: state.epochStartedAt,
  })
}
