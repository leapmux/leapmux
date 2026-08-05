import type { CheckpointDelta, ChunkRef, ChunkUpsert } from './checkpointStore'
import type { PendingOpsManager } from './pendingOps'
import type { UserCrdtState } from '~/generated/leapmux/v1/user_crdt_pb'
import type { WatchUserEvent } from '~/generated/leapmux/v1/user_ops_pb'
import { toBinary } from '@bufbuild/protobuf'
import { WatchUserEventSchema } from '~/generated/leapmux/v1/user_ops_pb'
import { createLogger } from '~/lib/logger'
import {
  entityKey,
  framedEntityKeys,
  fullCheckpointDelta,
  keysRemovedSince,
  parseEntityKey,
  serializeEntity,
  serializeHeader,
  snapshotEntityKeys,
} from './checkpointChunks'
import { appendOpLogSegment, MAX_OPLOG_READ_FRAMES, writeCheckpointAndTruncateOpLog } from './checkpointStore'

// ---------------------------------------------------------------------------
// Checkpoint recorder: the client's half of the compaction loop
//
// Owns everything between "a confirmed frame was applied" and "the persisted
// checkpoint + op-log reflect it": the serialize, the coalesced append, the
// frame counter, the compaction threshold, and the checkpoint rewrite.
//
// It is a plain object, not a Solid primitive, so the policy is testable
// without a reactive root — and, more to the point, so the counter and the
// microtask that drives it live in ONE scope. They were previously spread
// across three (a hook-scope record, a closure inside the manager installer,
// and a free function), which had already produced two shipped bugs: a shared
// generation counter that let a routine checkpoint flush cancel an unrelated
// in-flight hydration, and one that invalidated its own hydration on a direct
// user switch.
//
// THE REWRITE IS INCREMENTAL. It used to re-serialize the WHOLE
// `confirmedState` on every threshold trip — synchronously, on the main thread,
// every 256 confirmed frames, i.e. mid-drag: 7.1 ms at 400 nodes / 600 tabs and
// 56.7 ms at 2400 / 4800, to persist ops that touched one or two entities. So
// the recorder now tracks WHICH entities each confirmed frame touched and
// rewrites only those chunks (see checkpointChunks.ts for the split). The cost
// of a routine checkpoint is the delta plus an O(1) header, not the account.
// ---------------------------------------------------------------------------

const log = createLogger('checkpointRecorder')

/**
 * Frames appended since the last checkpoint before a rewrite is triggered.
 * Bounds the cold-reload replay, mirroring Manager.maybeCompact on the hub.
 */
export const CHECKPOINT_OP_LOG_THRESHOLD = 256

export interface CheckpointRecorderOptions {
  userId: string
  /** Per-tab CRDT client id; the checkpoint and op-log are scoped to it. */
  clientId: string
  mgr: PendingOpsManager
  /**
   * The persisted checkpoint this recorder inherits.
   *
   *   - A VALUE: hydration replayed a base that is already durable under this
   *     owner's key, so the first rewrite may be incremental against it.
   *   - A PROMISE: the base is being written right now (the sibling seed's
   *     adoption). The recorder HOLDS every append and rewrite until it
   *     settles -- see the `base` phase below for why that is not optional --
   *     and treats a resolved `undefined`, or a rejection, as "no base".
   *   - ABSENT: the store holds nothing usable for this owner (a cold start, or
   *     a wipe after a corrupt record), so the first rewrite must be FULL.
   *
   * Accepting the promise HERE, rather than exposing an `installBase()` the
   * caller must remember to call, is what makes the held window impossible to
   * skip: there is no way to construct a recorder over an in-flight base and
   * forget to tell it when the base lands.
   */
  hydratedFrom?: HydratedBase | Promise<HydratedBase | undefined>
  /** Test seam; defaults to CHECKPOINT_OP_LOG_THRESHOLD. */
  threshold?: number
}

/** A durable checkpoint base the recorder can rewrite incrementally against. */
export interface HydratedBase {
  /**
   * The op-log frames hydrate() replayed on top of the persisted chunks.
   *
   * They serve two purposes. (1) They are already ON DISK -- only a checkpoint
   * rewrite truncates the log -- so seeding the frame counter with them is what
   * makes the threshold trip and drain a log grown by a run of short
   * delta-resumed sessions that never bootstrap. (2) They are exactly what
   * moved the state past the persisted chunks, so the entities they touched are
   * the recorder's initial DIRTY SET.
   */
  frames: readonly WatchUserEvent[]
  /**
   * The ordinal the next appended segment must carry, from the store's read.
   * Seeding it is what keeps the persisted log's ordinal sequence contiguous
   * across a reload -- otherwise the first post-hydrate append would restart at
   * 0 behind segments already numbered 0..N-1, and the next cold start would
   * read that as a hole and discard a log that was in fact intact.
   */
  nextOrdinal: number
}

export interface CheckpointRecorder {
  /** Persist one confirmed frame. Best-effort; never throws. */
  record: (frame: WatchUserEvent) => void
  /**
   * A bootstrap replaced confirmedState wholesale, so the snapshot IS a perfect
   * checkpoint base and every accumulated frame describes discarded state.
   * Re-bases immediately, with a FULL rewrite — the previous lineage's chunks
   * are all invalid.
   */
  onCheckpointReset: () => void
  /** Rewrite the checkpoint now (used after a truncated or oversized replay). */
  rewriteNow: () => void
  /**
   * Stop recording and resolve once any in-flight IDB write has settled.
   *
   * Awaiting matters: the caller clears this account's rows on logout, and both
   * that clear and a queued append are unawaited IDB work against the same
   * cached connection. Without ordering them, an append armed just before the
   * logout could land AFTER the wipe and leave orphaned frames of a previous
   * account on a shared device — rows nothing would ever read or truncate,
   * since their checkpoint is gone.
   */
  dispose: () => Promise<void>
  /** Visible for testing: frames counted since the last successful rewrite. */
  readonly opLogCount: number
  /** Visible for testing: entity keys awaiting a chunk rewrite. */
  readonly dirtyKeys: ReadonlySet<string>
  /** Visible for testing: whether the next rewrite re-serializes everything. */
  readonly needsFullRewrite: boolean
}

export function createCheckpointRecorder(opts: CheckpointRecorderOptions): CheckpointRecorder {
  const { userId, clientId, mgr } = opts
  const threshold = opts.threshold ?? CHECKPOINT_OP_LOG_THRESHOLD

  const pendingFrames: Uint8Array[] = []
  let appendScheduled = false
  let disposed = false
  let opLogCount = 0
  /**
   * Ordinal for the next op-log segment. Advanced per ATTEMPTED flush, never per
   * successful one: numbering on success would give the next segment the number
   * a failed append would have used, sealing the gap that is the only evidence
   * the append was lost. Reset to 0 exactly where `opLogCount` is -- when a
   * checkpoint write RESOLVES ok, which is when the log on disk is actually
   * empty.
   */
  let nextOrdinal = 0
  /**
   * Entity keys (`kind:id`, the vocabulary checkpointChunks speaks) whose
   * persisted chunk no longer matches confirmedState. A rewrite serializes
   * exactly these — or deletes their chunk, when the entity is gone.
   */
  const dirty = new Set<string>()
  /**
   * What the persisted chunks are worth as a rewrite base. ONE variable with
   * three phases, not a pair of booleans, so the contradictory combination
   * ("still waiting for the base" AND "the base is good") cannot be written.
   *
   *   - `ready`  — the chunks on disk match a known point in this lineage, so
   *                the next rewrite may be INCREMENTAL against them.
   *   - `none`   — there is nothing usable underneath (a cold start, a wiped
   *                poison record, a failed adoption) or the lineage was
   *                invalidated, so the next rewrite must re-serialize EVERY
   *                entity and drop whatever else is on disk.
   *   - `pending` — a base is being written RIGHT NOW and we do not yet know
   *                which of the two it will become. Every append and every
   *                rewrite is HELD, because both would race that write: an
   *                append would mint ordinal 0 against a log the adoption is
   *                about to truncate and re-seed at ordinal 0 (two rows at the
   *                same ordinal, which the reader's contiguity check silently
   *                truncates at), and a rewrite would serialize a base the
   *                adoption is about to replace wholesale.
   *
   * Held frames are BUFFERED, never dropped -- see flushAppend. `pending` is
   * therefore a delay, not a data loss, and it ends on the first settle.
   */
  let base: 'pending' | 'none' | 'ready'
  /**
   * A rewrite was asked for while `base` was `pending`, and still owes an
   * answer.
   *
   * The hold has to DEFER the request, not drop it, and the two are easy to
   * confuse because the threshold-driven trigger re-arms itself on the next
   * flush. The one-shot triggers do not: `hydrateInto` calls `rewriteNow()`
   * exactly once, when the replayed log was truncated or already over the
   * threshold, and on the seed path that call lands while the adoption is still
   * in flight by construction. Without this flag it was refused and never
   * re-issued, so a seeded tab kept an over-ceiling log -- and, before the
   * validated-prefix copy, a poison tail -- on disk for its whole session,
   * re-truncating at the same frame on every later reload.
   */
  let heldRewrite = false
  /**
   * Frame count at which the next compaction is attempted. Pushed out by a
   * whole threshold whenever a rewrite is skipped or fails, so a store that
   * CANNOT compact (quota exhausted, private browsing) stops re-serializing on
   * every subsequent flush. Without it the counter stays above the threshold
   * forever and each flush re-runs a serialization that is already known to
   * fail.
   */
  let nextCompactAt = threshold
  /**
   * True from the moment a rewrite starts until its IDB write settles. The
   * counter is only cleared on success, so releasing the trigger earlier let
   * every flush in that window schedule ANOTHER serialization — several
   * back-to-back per threshold trip during a drag.
   */
  let writing = false
  /**
   * True while the persisted log is known NOT to describe the current lineage,
   * and no rewrite has yet succeeded in re-basing it. Blocks appends outright.
   *
   * Two producers, one hazard. (1) A frame that fails to serialize is dropped,
   * so everything after it would sit above a hole. (2) A bootstrap replaced
   * confirmedState wholesale, so the surviving log prefix describes the
   * DISCARDED lineage. In both cases appending the next frame onto the old rows
   * builds a log whose replay is a mix of two states, and whose trailing
   * `batchEnd` frames advance the replayed cursor past ops the hub -- which
   * ships only what is strictly after the cursor -- will never re-send.
   *
   * `rewriteCheckpoint` is best-effort: it early-returns while another write is
   * in flight or before a watermark exists, and its IDB write can resolve false
   * (quota, private browsing). So "we asked for a rebase" is not "the log is
   * clean"; only a write that RESOLVES ok clears this. Until then, dropping
   * frames is the safe direction -- the persisted pair stays older but
   * self-consistent, so the next cold start simply resumes from further back and
   * the hub fills the gap. Appending would instead produce a cursor that lies.
   */
  let needsRebase = false
  /**
   * Bumped every time the persisted lineage is invalidated. A rewrite captures
   * it before serializing, so a write that RESOLVES after an invalidation
   * cannot clear the flags that invalidation raised -- its blob describes the
   * superseded lineage. Without it, a bootstrap landing while a rewrite was in
   * flight had its `needsRebase` cleared by that write's success handler, and
   * the recorder resumed appending onto a checkpoint of the wrong lineage: the
   * precise failure the flag exists to prevent.
   */
  let lineage = 0
  /**
   * Every unsettled IDB write, so dispose() can order the caller's logout wipe
   * after ALL of them. A single slot was not enough: flushAppend assigns the
   * append and may then start a checkpoint rewrite, and the next flush would
   * overwrite the slot and orphan that rewrite -- whose `put` could then land
   * AFTER the wipe, leaving a serialized UserCrdtState for a logged-out
   * account on a shared device, with no op-log and nothing that would ever
   * truncate it.
   */
  let inFlight: Promise<unknown> = Promise.resolve()
  function track(write: Promise<unknown>): void {
    inFlight = Promise.allSettled([inFlight, write])
  }

  /**
   * The persisted pair no longer describes the live lineage: block appends,
   * force the next rewrite to be FULL, and supersede any write already in
   * flight.
   *
   * All three together, always. A dropped frame technically invalidates only
   * the log, but its entity keys may be exactly what could not be read off the
   * frame -- so a full rewrite is the honest recovery, and tying the three
   * makes "an invalidated lineage can never be re-based incrementally" true by
   * construction rather than by case analysis.
   */
  function invalidateLineage(): void {
    needsRebase = true
    // Also ABANDONS a pending base: whatever that write lands is superseded by
    // definition, and adoptBase refuses to install over a phase it no longer
    // owns. Appends stay blocked by needsRebase until a full rewrite succeeds,
    // so ending the hold early cannot let one race the adoption.
    base = 'none'
    // And DROPS whatever is queued, because those frames describe the lineage
    // this call just invalidated. Both `record` arms already cleared the queue
    // before reaching here; the arm that did not is `onCheckpointReset`, whose
    // frames could be sitting in the buffer because the `pending` HOLD parked
    // them there rather than appending them. Leaving them meant the next
    // recorded frame spliced pre-bootstrap bytes onto a log the bootstrap's own
    // rewrite had just truncated -- and the client applies materialized /
    // removed WHOLESALE, with no HLC compare, so replaying them on the next
    // cold start reinstates discarded records for good.
    //
    // Clearing HERE rather than at each caller is what makes "an invalidated
    // lineage can never be re-based incrementally" cover the queue too, by
    // construction instead of by case analysis.
    pendingFrames.length = 0
    lineage++
  }

  /** Mark every entity `frame` touched, or the whole state if its shape is unknown. */
  function markDirty(frame: WatchUserEvent): void {
    let keys: Set<string> | null
    try {
      keys = framedEntityKeys(frame)
    }
    catch {
      // A structurally malformed frame -- an `ops` field that is not iterable,
      // a oneof value that is not a message. `framedEntityKeys` reads it
      // directly, and record() promises never to throw, so a frame this broken
      // is treated exactly like one whose shape is not understood.
      keys = null
    }
    if (keys === null) {
      // Under-reporting is the one unrecoverable direction: an entity whose
      // chunk is stale on disk but absent from the dirty set is never
      // rewritten, so the next cold start resurrects the pre-change record
      // silently. A frame shape this build does not understand therefore costs
      // one full rewrite rather than a guess.
      invalidateLineage()
      return
    }
    for (const key of keys)
      dirty.add(key)
  }

  /**
   * The chunk writes and deletes that bring the persisted shards up to `state`,
   * or null when a dirty key cannot be resolved to a chunk — the caller then
   * falls back to a full rewrite, since an unresolvable key is exactly "one of
   * the shards changed and this pass cannot say which".
   */
  function incrementalDelta(state: UserCrdtState, keys: Iterable<string>): CheckpointDelta | null {
    const upserts: ChunkUpsert[] = []
    const deletes: ChunkRef[] = []
    for (const key of keys) {
      const ref = parseEntityKey(key)
      if (!ref)
        return null
      const bytes = serializeEntity(state, ref)
      // Absent from the state: removed by an op or collected by the tombstone
      // prune, so its chunk has to go with it.
      if (bytes === undefined)
        deletes.push(ref)
      else
        upserts.push({ ...ref, bytes })
    }
    return { headerBytes: serializeHeader(state), upserts, deletes, full: false }
  }

  /**
   * Compact, then decide what the next checkpoint write should contain: an
   * incremental delta over the persisted chunks, or a full re-serialization.
   * Returns null when nothing can be built, and the caller backs off.
   *
   * Extracted because the PRUNE and the KEY SNAPSHOT BRACKETING IT are the
   * subtlest coupling in this file, and inline they read as two of six things
   * `rewriteCheckpoint` was doing. The prune drops tombstone shells whose
   * tombstoning op may have been recorded many checkpoints ago, so the dirty set
   * cannot see them -- the bracket is the only thing that turns those removals
   * into chunk DELETES, and without it their chunks survive on disk and the next
   * cold start resurrects every shell the prune just collected. Naming the unit
   * gives that pairing one place to be got right.
   *
   * NOT pure, and deliberately not pretending to be: it runs the compaction and
   * it may demote `base` and clear `dirty` when a key will not resolve. What it
   * does not do is issue the write or touch anything the write's resolution
   * owns (`writing`, `nextCompactAt`, `needsRebase`).
   */
  function buildDelta(): CheckpointDelta | null {
    try {
      // Guarded for the same reason the frame serializer is: this runs
      // synchronously inside the manager's bootstrap reset observer, so a throw
      // would propagate out through bootstrap() -> applyMaterialized() -> the
      // WebSocket message handler, skipping setBootstrapped and every other
      // post-bootstrap step while the socket stays healthy and reports nothing.
      // Best-effort is what the caller promises; the guard is what makes it
      // true.
      //
      // Prune first, so the chunks about to be written describe the SMALLER
      // state. This is the client's compaction step and it is paired with the
      // checkpoint rewrite exactly as the hub pairs PruneTombstonesAtOrBelow
      // with its state_payload write -- pruning without persisting would be
      // undone by the next cold reload, which replays from the persisted state.
      const before = base === 'ready' ? snapshotEntityKeys(mgr.state.confirmedState) : null
      mgr.compactTombstones()
      const state = mgr.state.confirmedState
      if (before) {
        for (const ref of keysRemovedSince(before, state))
          dirty.add(entityKey(ref.kind, ref.entityId))
      }
      const incremental = base === 'ready' ? incrementalDelta(state, dirty) : null
      if (incremental)
        return incremental
      // Either the lineage was already invalidated, or a dirty key could not
      // be resolved -- both mean the persisted shards cannot be brought up to
      // date one entity at a time.
      if (base === 'ready') {
        log.warn('a dirty entity key could not be resolved; rewriting the whole checkpoint')
        base = 'none'
        dirty.clear()
      }
      return fullCheckpointDelta(state)
    }
    catch (err) {
      log.warn('failed to serialize confirmed state for checkpoint; skipping', { err })
      return null
    }
  }

  /** Returns whether a checkpoint write was actually ISSUED. */
  function rewriteCheckpoint(): boolean {
    if (disposed || writing)
      return false
    if (base === 'pending') {
      // A rewrite would serialize a base the in-flight adoption is about to
      // replace wholesale, and its truncate would race the adoption's own.
      // DEFER it rather than drop it -- adoptBase re-issues whatever this flag
      // still owes. The threshold-driven trigger would re-arm itself on the
      // next flush, but the one-shot `rewriteNow()` hydrateInto fires for a
      // truncated or over-threshold log would not, and that is precisely the
      // call the seed path makes while this hold is up.
      heldRewrite = true
      return false
    }
    // Past the hold: whatever was owed is being answered now, whether this
    // attempt issues a write or backs off below.
    heldRewrite = false
    const wm = mgr.state.resumeWatermark
    if (!wm) {
      // Nothing to pin the checkpoint at yet. Back off rather than re-checking
      // on every single flush.
      nextCompactAt = opLogCount + threshold
      return false
    }
    // Captured BEFORE the serialize: anything that invalidates the lineage from
    // here on -- including during `compactTombstones` -- must supersede the
    // write this call is about to issue, not be cleared by it.
    const writtenLineage = lineage
    const delta = buildDelta()
    if (!delta) {
      nextCompactAt = opLogCount + threshold
      return false
    }
    // Captured AFTER the delta is built, so it covers the keys the tombstone
    // diff just added. Taken before the serialize it left them resident for the
    // rest of the session, re-emitting a delete for an already-deleted chunk on
    // every subsequent rewrite and growing the set without bound.
    const writtenKeys = [...dirty]
    writing = true
    track(writeCheckpointAndTruncateOpLog(userId, clientId, delta, wm, mgr.state.currentEpoch)
      .then((ok) => {
        writing = false
        if (!ok) {
          // Nothing landed: the dirty set and the full-rewrite flag must stay
          // exactly as they were, or the entities this attempt described would
          // never be written again.
          nextCompactAt = opLogCount + threshold
          return
        }
        // The truncate landed, so the on-disk log is empty whichever lineage
        // the chunks describe. This is the ONLY place that becomes true, which
        // is why the counter is not zeroed anywhere else.
        opLogCount = 0
        nextOrdinal = 0
        nextCompactAt = threshold
        if (lineage !== writtenLineage) {
          // Invalidated after this write serialized. Its chunks describe the
          // superseded lineage, so leave needsRebase and the base phase standing
          // and let the retry re-base with a FULL rewrite.
          return
        }
        needsRebase = false
        base = 'ready'
        // Only the keys this write actually serialized. Frames recorded WHILE
        // it was in flight dirtied entities the chunks on disk do not describe,
        // so clearing the whole set would strand them.
        for (const key of writtenKeys)
          dirty.delete(key)
      })
      .catch(() => {
        writing = false
        nextCompactAt = opLogCount + threshold
      }))
    return true
  }

  function flushAppend(): void {
    appendScheduled = false
    if (disposed)
      return
    if (base === 'pending') {
      // HOLD, do not clear: unlike the needsRebase arm below, these frames are
      // not describing a superseded lineage -- we simply do not know their
      // ordinal yet. adoptBase re-enters here the moment the base settles, and
      // dropping them instead would lose ops that are already in confirmedState
      // and will never be re-sent (the hub ships only what is strictly after
      // the cursor).
      return
    }
    if (needsRebase) {
      // The lineage was invalidated after these frames were queued but before
      // the microtask ran. Appending them now would put frames of the old
      // lineage on disk AFTER the rebase was decided -- exactly what
      // `needsRebase` blocks in `record`, reached one turn later.
      pendingFrames.length = 0
      return
    }
    const batch = pendingFrames.splice(0)
    if (batch.length === 0)
      return
    // When THIS flush alone trips compaction, skip the append entirely: the
    // rewrite about to run serializes confirmedState -- which already includes
    // every one of these frames' effects, since `record` runs after apply -- and
    // truncates the log in the same transaction. Appending first would write
    // bytes whose only reader is a truncate microseconds later.
    //
    // That is not a rare corner. A resume delta carries up to MaxResumeDeltaOps
    // (5000) frames in one burst, ~20x the 256-frame threshold, so the reconnect
    // path -- the one #267 exists to make cheap -- paid ~11 ms of synchronous
    // main-thread `toBinary` plus a ~191 KiB IDB write, both immediately
    // discarded.
    //
    // Safe when the rewrite's write LATER fails: nothing truncates, so the
    // persisted pair stays at its previous, self-consistent point, and the next
    // cold start simply resumes from further back with the hub filling the gap.
    // That is the same trailing-staleness the ordinal contract already permits
    // -- unlike a HOLE, which is what appending-then-failing would risk.
    if (opLogCount + batch.length >= nextCompactAt && rewriteCheckpoint())
      return
    // One flush = one op-log SEGMENT row (mirroring the backend's
    // one-row-per-batch), so a burst of N frames is 1 IDB transaction + 1 row,
    // not N.
    //
    // Bounded at WRITE time, not just at read time. `MAX_OPLOG_READ_FRAMES` is
    // enforced by the reader, so a log past it is guaranteed to be truncated on
    // the next cold start -- every row beyond the ceiling is storage written to
    // be discarded. The regime that reaches it is real: a profile at quota where
    // small `add`s keep succeeding while the multi-megabyte checkpoint `put`
    // keeps failing, so the back-off only pushes `nextCompactAt` out and the log
    // grows a threshold's worth per failed retry, forever.
    if (opLogCount >= MAX_OPLOG_READ_FRAMES) {
      log.warn('op-log is past the read ceiling and the checkpoint will not write; dropping appends until it does', { opLogCount })
      rewriteCheckpoint()
      return
    }
    track(appendOpLogSegment(userId, clientId, batch, nextOrdinal++))
    opLogCount += batch.length
    if (opLogCount >= nextCompactAt)
      rewriteCheckpoint()
  }

  /**
   * Install (or refuse) the base once it is durable, then release everything
   * that was held while we waited.
   *
   * Refuses to INSTALL when the phase is no longer `pending`: an
   * invalidateLineage in the meantime means this base describes a superseded
   * lineage, and installing it would re-arm incremental rewrites against chunks
   * a bootstrap already discarded.
   *
   * The RELEASE runs either way, and that separation is the point. Returning
   * early on the refusal left the queue neither appended nor cleared and
   * nothing scheduled to revisit it -- every held flush had already set
   * `appendScheduled = false` -- so on a tab that went quiet after its
   * reconnect burst the bytes simply sat there for the session.
   */
  function adoptBase(settled: HydratedBase | undefined): void {
    if (disposed)
      return
    // `base === 'pending'` is an EXACT proxy for "nothing invalidated the
    // lineage since this base was claimed", and that is provable locally rather
    // than by inspection: only the two constructor arms ever assign `pending`,
    // so the phase is never re-entered, and `rewriteCheckpoint` refuses to issue
    // while it holds -- so no write-success handler can be in flight to promote
    // or demote it either. `invalidateLineage` is the single transition out.
    if (base === 'pending') {
      if (settled) {
        opLogCount = settled.frames.length
        nextOrdinal = settled.nextOrdinal
        // The persisted chunks are the state hydrate() started from; these
        // frames are everything that moved it since, so their entities are the
        // chunks that no longer match. A frame whose shape is not understood
        // invalidates the lineage here exactly as it would live -- which is why
        // this runs BEFORE the phase is promoted, so that invalidation wins.
        for (const frame of settled.frames)
          markDirty(frame)
      }
      if (base === 'pending')
        base = settled ? 'ready' : 'none'
    }
    // Release the held frames. Safe on BOTH arms: with a base, they continue
    // its ordinal sequence; without one, the adoption wrote nothing at all
    // (its transaction aborted), so the log is empty and ordinal 0 is free.
    flushAppend()
    // Then answer any rewrite the hold deferred. AFTER flushAppend, because
    // that call may already have tripped the threshold and issued one -- in
    // which case the flag is clear and this is a no-op.
    if (heldRewrite)
      rewriteCheckpoint()
  }

  if (opts.hydratedFrom instanceof Promise) {
    base = 'pending'
    void opts.hydratedFrom.then(adoptBase, (err: unknown) => {
      // A base write that REJECTED is a base that is not there. Treated exactly
      // like a resolved `undefined`: the alternative -- leaving the recorder
      // held forever -- would silently stop persisting for the whole session.
      log.warn('the persisted base never landed; recording against a full rewrite', { err })
      adoptBase(undefined)
    })
  }
  else if (opts.hydratedFrom) {
    base = 'pending'
    adoptBase(opts.hydratedFrom)
  }
  else {
    base = 'none'
  }

  return {
    record(frame: WatchUserEvent): void {
      if (disposed)
        return
      // Dirty-mark FIRST, and UNCONDITIONALLY -- before the `needsRebase` gate,
      // not after it. The frame's effect is already in confirmedState, so its
      // entities' chunks are stale regardless of whether the frame itself can
      // be appended to the op-log, and regardless of whether a rebase is
      // pending.
      //
      // Returning early without marking lost the frame outright. A rebase
      // rewrite serializes its delta from confirmedState, THEN awaits IDB; a
      // frame arriving in that window was neither in the serialized bytes nor
      // in `dirty` nor in the op-log (blocked below), yet the write's success
      // handler cleared `needsRebase` and promoted the base because its `lineage` still
      // matched. The entity then sat stale on disk with nothing left to rewrite
      // it, while the next rewrite pinned a watermark PAST the lost ops -- which
      // the hub never re-ships, since it sends only what is strictly after the
      // cursor. A tile split or tab move made in the milliseconds after a
      // reconnect FALLBACK vanished on the next page load and never came back.
      //
      // Marking here is sufficient because the write's success handler deletes
      // only `writtenKeys` -- the set captured BEFORE it serialized -- so keys
      // dirtied while it was in flight survive and are picked up by the next
      // incremental rewrite. That mechanism already existed; this gate was
      // routing around it.
      markDirty(frame)
      if (needsRebase) {
        // Either the log was already known stale, or `markDirty` could not
        // describe this frame and just invalidated the lineage. Same exit
        // either way: do NOT append onto stale rows, and keep retrying the
        // rebase (it may have been blocked by an in-flight write or a missing
        // watermark). A checkpoint written at the CURRENT confirmedState
        // already includes this frame's effect.
        pendingFrames.length = 0
        rewriteCheckpoint()
        return
      }
      // Serialize synchronously so the persisted bytes are a stable snapshot of
      // the frame as applied. A throw here would propagate back through
      // record() into the consume* entry points and interrupt their post-record
      // work (consumeBatchCommitted splices its pending batch AFTER record, so a
      // throw would strand it), hence the guard.
      let bytes: Uint8Array
      try {
        bytes = toBinary(WatchUserEventSchema, frame)
      }
      catch (err) {
        // Dropping the frame and carrying on would leave the persisted log
        // holding frames from BOTH sides of the gap, and the post-gap
        // `batchEnd` frames would advance the replayed cursor past the ops that
        // went missing -- which the hub never re-ships, because it sends only
        // what is strictly after the cursor. Stop appending and re-base
        // instead: a checkpoint written at the CURRENT confirmedState already
        // includes this frame's effect, so it is the self-healing exit.
        log.warn('failed to serialize confirmed frame for op-log; re-basing the checkpoint', { err })
        // invalidateLineage drops the queue: these frames belong to the lineage
        // this drop just invalidated.
        invalidateLineage()
        rewriteCheckpoint()
        return
      }
      pendingFrames.push(bytes)
      if (!appendScheduled) {
        appendScheduled = true
        queueMicrotask(flushAppend)
      }
    },
    onCheckpointReset(): void {
      if (disposed)
        return
      // Do NOT zero opLogCount here. The rows are still on disk until the
      // truncating write resolves ok, and the counter is the only trigger for
      // the next compaction attempt -- zeroing it up front hid N real rows and
      // pushed the retry a full threshold away, so a client whose writes keep
      // failing grew a log far past its bound while reporting none. The write's
      // own handler already applies the correct rule.
      //
      // The dirty set is not cleared either, for the same reason: it is
      // subsumed by the full rewrite `invalidateLineage` forces, and clearing
      // it would matter only if that rewrite failed -- exactly when the stale
      // chunks it names are still on disk.
      invalidateLineage()
      rewriteCheckpoint()
    },
    rewriteNow(): void {
      rewriteCheckpoint()
    },
    async dispose(): Promise<void> {
      disposed = true
      pendingFrames.length = 0
      await inFlight.catch(() => {})
    },
    get opLogCount(): number {
      return opLogCount
    },
    get dirtyKeys(): ReadonlySet<string> {
      return dirty
    },
    get needsFullRewrite(): boolean {
      return base !== 'ready'
    },
  }
}
