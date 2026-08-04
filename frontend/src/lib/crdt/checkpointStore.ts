import type { HlcShape } from './hlc'
import type { IdbSchema } from '~/lib/idb'
import {
  createIdbConnection,
  forEachCursor,
  forEachCursorWhile,
  isIndexedDbAvailable,
  requestToPromise,
  selectSweepVictims,
  txToPromise,
} from '~/lib/idb'
import { createLogger } from '~/lib/logger'

// ---------------------------------------------------------------------------
// Persistent (IndexedDB) CRDT checkpoint + op-log store
//
// Delta-resume across a page refresh / app restart needs the materialized
// `confirmedState` to survive the JS heap being discarded, plus the confirmed
// frames applied since the checkpoint so the client can re-reach the tight
// resume cursor `T_now`. This store persists both, mirroring the hub's
// `user_state` (the checkpoint) + `user_op_batches` (the op log) pair.
//
// Three object stores in one DB:
//   - `checkpoints` (one row per OWNER): the checkpoint's METADATA at
//     checkpoint time T_c -- the serialized state HEADER (scalars only), the
//     resume watermark pinned to T_c, and the epoch. On cold reload, hydrate()
//     restores this plus the chunks below as the base the op-log replays onto.
//   - `checkpointChunks` (one row per ENTITY of an owner): one serialized
//     record each. The state is sharded rather than stored as one blob so a
//     rewrite costs the DELTA, not the account: the old design re-ran
//     `toBinary` over the whole UserCrdtState on the main thread every 256
//     confirmed frames (7.1 ms at 400 nodes / 600 tabs, 56.7 ms at 2400 /
//     4800), landing mid-drag, to persist a change to one or two entities.
//   - `opLog` (one row per SEGMENT since the checkpoint): each row holds the
//     batch of confirmed WatchUserEvent frames appended in one flush, in apply
//     order. Coalescing a flush's N frames into one row (rather than N) cuts
//     the per-burst IDB transaction + row count to 1, which matters on the
//     confirmed-mutation hot path (a drag is ~120 frames/sec, half of them
//     self-traffic) and on the truncate path.
//
// AN OWNER IS A (user, client) PAIR, NOT A USER.
//
// Keying by user alone made every tab of that user share one checkpoint and one
// op-log, with no leader election and no lock. `writeCheckpointAndTruncateOpLog`
// deletes the whole log, so a BACKGROUNDED tab -- whose confirmedState lags,
// because throttled timers delay nothing about the frames it has yet to apply --
// could write its stale state@T1 and delete the segments a foreground tab had
// appended for frames after T1. The next cold reload then replays state@T1 plus
// only the post-truncate frames, with the T1..T2 range gone, and the `batchEnd`
// frames in that replayed tail advance the resume cursor straight past the
// hole. The hub ships only what is strictly after the cursor, so the lost ops
// are never re-sent: silent, permanent divergence. (The mirror direction is
// worse in kind: `entityRemoved` is applied wholesale with no HLC compare, so a
// stale one replayed over a newer checkpoint deletes a live record.)
//
// The client id is per-tab (sessionStorage) and SURVIVES a refresh, so scoping
// per tab keeps the case #356 exists for -- reload the same tab, resume from
// its own checkpoint -- while making cross-tab clobber UNLIKELY. Not
// impossible: browsers COPY sessionStorage into a duplicated tab, so two live
// tabs can hold the same id, and the only thing that separates them again is
// clientIdentity.ts's asynchronous BroadcastChannel handshake, which re-mints
// AFTER the fact and is unavailable in older Safari and some embedded webviews
// -- where the duplicate keeps the incumbent's owner key for good.
//
// The cost is one checkpoint (header plus its chunks) per open tab. The
// alternatives -- a Web Locks leader, or HLC-bounded truncation with a
// monotonic compare-and-set checkpoint -- buy SHARED storage instead. That is
// no longer a question of whether a cross-tab protocol is affordable (this
// branch added one, and the sweep now consults it for liveness); it is that
// neither survives the unguarded `entityRemoved` replay above.
//
// A genuinely NEW tab therefore has no checkpoint of its own, sends no resume
// cursor, and cold-starts with a full snapshot -- which is the last common
// connect still paying the projection-lock scan #267 exists to remove. The
// clobber above is a WRITE hazard, so a new tab could safely SEED from a
// sibling's checkpoint read-only and resume, writing only ever under its own
// id. Tracked in https://github.com/leapmux/leapmux/issues/358, which also
// records why #267's other two mitigations were rejected. Measure before
// building it.
//
// The store is a PURE BLOB STORE: it returns raw `Uint8Array` and never
// deserializes proto here -- a chunk's `kind` and `entityId` are opaque strings
// to it, and checkpointChunks.ts owns what they mean. The corruption policy
// lives in hydrate.ts, isolating the proto-coupling upstream so this module
// stays proto-agnostic and the parse/replay path is unit-testable without IDB.
//
// Every operation is best-effort and no-throw: without indexedDB (jsdom, SSR,
// private browsing) or when it fails (quota), reads miss and writes drop. A
// miss always degrades to a full snapshot — the correct fallback. The open /
// cache / upgrade / test-reset scaffold is shared with the app's other IDB
// store via `~/lib/idb` (no `idb` dependency).
// ---------------------------------------------------------------------------

const log = createLogger('checkpointStore')

const DB_NAME = 'leapmux-crdt-state'
// A CONSTANT. Schema changes land by editing SCHEMA below, not by bumping this:
// the scaffold checks every opened database against that declaration and
// rebuilds any that does not match. See ~/lib/idb's header for why recreating
// rather than migrating is the permanent policy -- these stores are a cache
// over state the hub owns and re-syncs, so a rebuild costs one cold start.
const DB_VERSION = 1
const CHECKPOINT_STORE = 'checkpoints'
const CHUNK_STORE = 'checkpointChunks'
const OPLOG_STORE = 'opLog'
/** Index over the owning (user, client) pair — the replay and truncate path. */
const BY_OWNER_INDEX = 'byOwner'
/** Index over the user alone — the logout wipe, which spans every client. */
const BY_USER_INDEX = 'byUserId'
/**
 * Index over `lastSeenAt` — the abandoned-owner sweep.
 *
 * Indexed rather than read off the records because the sweep must NOT
 * materialize what it is deciding about: a checkpoint row carries the state
 * header, so walking the rows to read a timestamp would deserialize every
 * abandoned tab's payload just to decide to delete it. An index key cursor
 * yields (lastSeenAt, [userId, clientId]) and touches no value at all. That
 * matters less than it did when the row held the WHOLE state, but the sweep is
 * still the one caller with no reason to read a value at all.
 */
const BY_LAST_SEEN_INDEX = 'byLastSeenAt'

/**
 * Owners whose checkpoint has not been rewritten within this window are swept.
 * Comfortably longer than the hub's op-retention window, so an expired
 * checkpoint could not have delta-resumed anyway — which is what makes the TTL
 * arm safe to apply to EVERY account's rows, not just the signed-in one's.
 *
 * A tab that is still open is exempt regardless of age; see the liveness probe
 * in `sweepAbandonedCheckpoints`.
 */
export const CHECKPOINT_TTL_MS = 14 * 24 * 60 * 60 * 1000

/**
 * Cap on retained owners per user, enforced oldest-first. The TTL alone does
 * not bound a user who opens tabs faster than they expire.
 */
export const CHECKPOINT_MAX_OWNERS = 8

/**
 * How often a running tab refreshes its own `lastSeenAt`.
 *
 * This is a single-row `put` on the metadata row -- no header, no chunks -- so
 * it is orders of magnitude cheaper than a checkpoint rewrite and can run on a
 * timer without touching the interaction path.
 */
export const OWNER_TOUCH_INTERVAL_MS = 60 * 1000

/**
 * How recently an owner must have been touched to count as RUNNING.
 *
 * Three touch intervals, so a tab has to miss three in a row before the sweep
 * may collect it. Browsers throttle timers in background tabs (and stop them
 * entirely for a frozen or bfcached page), and the cost of guessing wrong is
 * asymmetric: treating a live tab as dead destroys its checkpoint and op-log,
 * while treating a dead one as live only defers its reclamation by a few
 * minutes. The margin buys the cheap error.
 */
export const OWNER_LIVENESS_WINDOW_MS = 3 * OWNER_TOUCH_INTERVAL_MS

/**
 * Ceilings on ONE op-log read, mirroring the hub's MaxResumeDeltaOps (5000) and
 * MaxResumeDeltaBytes (4 MiB) so both halves of the resume path bound the same
 * quantities.
 *
 * Enforced AT THE CURSOR, not after it. The log is appended on the confirmed
 * hot path (~120 frames/sec during a drag) and drained only by a checkpoint
 * rewrite, which backs off whenever the write fails (quota, private browsing)
 * or no watermark exists yet -- so an unbounded read materializes an unbounded
 * log into memory on the very cold start that is meant to be cheap. Stopping
 * short reports `truncated`, which routes to the caller's checkpoint rewrite
 * and is exactly what drains the oversized log.
 */
export const MAX_OPLOG_READ_FRAMES = 5000
export const MAX_OPLOG_READ_BYTES = 4 * 1024 * 1024

/** On-disk shape of a checkpoint METADATA row (one per (user, client) pair). */
export interface CheckpointRecord {
  /** Owning user; also the logout-wipe key. */
  userId: string
  /** Owning tab's CRDT client id (per-tab, survives refresh). */
  clientId: string
  /**
   * The serialized state HEADER at T_c: `UserCrdtState` with every entity map
   * emptied (see checkpointChunks.serializeHeader). O(1) in account size, so
   * every rewrite can afford to re-serialize it; the entities live in
   * `checkpointChunks` rows and are rewritten only when they change.
   */
  headerBytes: Uint8Array
  /** The resume watermark pinned at checkpoint write time (T_c). */
  watermark: HlcShape
  /** The epoch the watermark was seen under. */
  currentEpoch: bigint
  /** Wall-clock time (ms) of the checkpoint WRITE itself. Diagnostics only. */
  writtenAt: number
  /**
   * Wall-clock time (ms) this owner was last known to be RUNNING.
   *
   * Deliberately separate from `writtenAt`, which moves only on a rewrite --
   * i.e. once per CHECKPOINT_OP_LOG_THRESHOLD confirmed frames. That made it a
   * bad liveness proxy: a quiet but live sibling tab looked arbitrarily stale,
   * and the sweep's cap arm deleted its checkpoint and op-log out from under
   * it. `touchOwner` refreshes this on a short interval and at hydration, so a
   * running tab is recent by construction and the sweep can decide liveness
   * from data it already reads instead of from a cross-tab round trip that a
   * backgrounded or frozen tab could never answer in time.
   *
   * It is also the right clock for the abandonment TTL: "this owner has not
   * been USED in 14 days" is the question the TTL means to ask, and `writtenAt`
   * answered a different one.
   */
  lastSeenAt: number
}

/** On-disk shape of one entity chunk row. */
export interface CheckpointChunkRecord {
  userId: string
  clientId: string
  /** Opaque to this module; checkpointChunks.ts maps it to a record schema. */
  kind: string
  /** The entity's map key within its kind. */
  entityId: string
  /** `toBinary(<kind's record schema>, record)`. */
  bytes: Uint8Array
}

/** One chunk's identity within an owner's checkpoint. */
export interface ChunkRef {
  kind: string
  entityId: string
}

/** A chunk to write. */
export interface ChunkUpsert extends ChunkRef {
  bytes: Uint8Array
}

/**
 * What one checkpoint rewrite changes: the (always re-written) header, plus the
 * chunks that moved since the last one.
 *
 * `full` is the bootstrap / lineage-reset arm: every chunk the owner already
 * has is dropped before the upserts land, so the persisted shards describe
 * exactly the state that produced them and no record of a discarded lineage can
 * survive. An incremental rewrite instead leaves untouched chunks alone, which
 * is the entire point -- the write scales with the delta, not the account.
 */
export interface CheckpointDelta {
  headerBytes: Uint8Array
  upserts: readonly ChunkUpsert[]
  deletes: readonly ChunkRef[]
  full: boolean
}

/** On-disk shape of one op-log SEGMENT row (one per flush since checkpoint). */
interface OpLogRecord {
  /**
   * Generator-assigned, store-wide monotonic sequence. It does NOT need to be
   * per-owner contiguous: replay walks the byOwner index, and an index cursor
   * yields equal-index-key records in PRIMARY-KEY order, so a globally
   * increasing seq still orders one owner's segments by append time. Letting
   * the store assign it removes the read-modify-write (a reverse cursor to find
   * the current max) that the append path used to pay on every flush, and with
   * it the prose argument for why that read and its write were atomic.
   */
  seq?: number
  userId: string
  clientId: string
  /**
   * Per-owner position of this segment, 0-based and CONTIGUOUS, restarting at 0
   * after each successful checkpoint write (which truncates the log).
   *
   * `seq` orders segments but cannot detect a MISSING one — it is store-wide, so
   * any gap in an owner's subsequence is indistinguishable from another owner's
   * rows having taken those numbers. That made a hole undetectable at replay,
   * and a hole is not a benign staleness: `readOpLogFrames` would return frames
   * from both sides of it, and the post-hole `batchEnd` frames advance the
   * resume cursor past the ops in the gap, which the hub never re-ships because
   * it sends only what is strictly after the cursor. Silent, permanent
   * divergence.
   *
   * The writer increments this per ATTEMPTED flush, never per successful one:
   * numbering on success would hand the next segment the number the failed one
   * would have used, closing the gap and hiding exactly the event this exists to
   * expose.
   */
  ordinal: number
  /** The batch of confirmed frame byte-blobs appended in one flush, in order. */
  framesBytes: Uint8Array[]
}

/**
 * Result of reading an owner's persisted pair.
 *
 * `miss` and `failed` are deliberately distinct. Collapsing them (the store
 * returning `undefined` for both) meant a single unreadable op-log row --
 * a malformed `framesBytes` that throws when flattened, or any transient
 * cursor error -- was reported as "there is no checkpoint", so hydrate.ts's
 * corruption policy never saw it and could not apply the prefix-truncate arm it
 * was written for. The checkpoint got discarded and the connect took the full
 * projection scan #267 exists to avoid.
 *
 * The arms are:
 *   - `miss`   — nothing persisted for this owner.
 *   - `failed` — the checkpoint metadata row or its chunks could not be read;
 *     the base is unusable, so the caller wipes and cold-starts. The chunks
 *     belong to this arm and NOT to the op-log's: they are half the base, and a
 *     partially-read chunk set would hydrate a state missing entities the
 *     resume cursor claims are present.
 *   - `ok`     — the checkpoint is valid. `opLogTruncated` then says whether
 *     `opLogFrames` is the WHOLE log or only a (possibly empty) prefix -- a
 *     prefix because a segment was unreadable, or because the read hit
 *     MAX_OPLOG_READ_FRAMES / MAX_OPLOG_READ_BYTES. Either way the caller
 *     should rewrite the checkpoint, which drops the rest from disk rather
 *     than re-failing (or re-truncating) every reload.
 */
export type CheckpointRead
  = | { status: 'miss' }
    | { status: 'failed' }
    | {
      status: 'ok'
      checkpoint: CheckpointRecord
      /** Every entity chunk for this owner, in no particular order. */
      chunks: CheckpointChunkRecord[]
      opLogFrames: Uint8Array[]
      opLogTruncated: boolean
      /**
       * The ordinal the NEXT appended segment must carry — one past the last
       * contiguously-read one, or 0 when nothing was read. The recorder seeds
       * its counter from this so numbering survives a reload without the append
       * path having to read the log back.
       */
      opLogNextOrdinal: number
    }

/** Whether persistence can work here at all — callers short-circuit synchronously on false. */
export function isCheckpointStoreAvailable(): boolean {
  return isIndexedDbAvailable()
}

/**
 * The database's shape, in one place.
 *
 * Both halves of the scaffold's schema handling derive from this: it builds the
 * stores on creation, and an opened database that does not match it is deleted
 * and rebuilt. Add an index here and existing local databases repair themselves
 * on the next open -- there is no second list to remember to update, and no
 * version to bump.
 */
const SCHEMA: IdbSchema = {
  [CHECKPOINT_STORE]: {
    // The owner tuple IS the primary key, so an owner-scoped read or delete
    // needs no index.
    keyPath: ['userId', 'clientId'],
    indexes: {
      [BY_USER_INDEX]: 'userId',
      [BY_LAST_SEEN_INDEX]: 'lastSeenAt',
    },
  },
  [CHUNK_STORE]: {
    // (owner, kind, entityId): a single chunk is addressed by primary key, so
    // an incremental rewrite needs no index at all for its puts and deletes.
    // The two index-driven walks below mirror the op-log's exactly -- an
    // owner-scoped drop (a FULL rewrite, corruption recovery, the sweep) and a
    // user-scoped drop (logout).
    keyPath: ['userId', 'clientId', 'kind', 'entityId'],
    indexes: {
      [BY_OWNER_INDEX]: ['userId', 'clientId'],
      [BY_USER_INDEX]: 'userId',
    },
  },
  [OPLOG_STORE]: {
    // autoIncrement: the store assigns seq, so append is a single `add` and
    // segment ordering is a property the store guarantees rather than one the
    // append path has to argue for.
    keyPath: 'seq',
    autoIncrement: true,
    indexes: {
      [BY_OWNER_INDEX]: ['userId', 'clientId'],
      [BY_USER_INDEX]: 'userId',
    },
  },
}

const connection = createIdbConnection(DB_NAME, DB_VERSION, SCHEMA)

const openDb = connection.open

/** Visible for testing: forget the cached connection (e.g. after swapping the IDBFactory). */
export function _resetCheckpointStoreForTest(): void {
  connection.reset()
}

/**
 * Read the checkpoint metadata, its entity chunks and its op-log for one owner.
 * Never throws; see CheckpointRead for how a miss, a failed base read, and a
 * partially readable op-log are told apart.
 */
export async function readCheckpoint(userId: string, clientId: string): Promise<CheckpointRead> {
  if (!isCheckpointStoreAvailable())
    return { status: 'miss' }
  let db: IDBDatabase
  let checkpoint: CheckpointRecord | undefined
  let chunks: CheckpointChunkRecord[]
  let tx: IDBTransaction
  try {
    db = await openDb()
    // Read the metadata row, its chunks and the op-log in ONE transaction so
    // the three stay consistent: a concurrent writeCheckpointAndTruncateOpLog
    // cannot land between the reads and leave a header describing chunks that
    // have moved, or a checkpoint whose op-log was just truncated.
    //
    // Every await below is on a request belonging to THIS transaction, which is
    // what keeps it active across the microtask checkpoint (a transaction stays
    // active while its own requests are outstanding). Do NOT introduce an await
    // on anything else between here and the op-log read: that would let the
    // transaction auto-commit, and the next objectStore() call would throw.
    tx = db.transaction([CHECKPOINT_STORE, CHUNK_STORE, OPLOG_STORE], 'readonly')
    checkpoint = await requestToPromise<CheckpointRecord | undefined>(
      tx.objectStore(CHECKPOINT_STORE).get([userId, clientId]),
    )
    if (checkpoint === undefined)
      return { status: 'miss' }
    // The chunks are the other half of the BASE, so an unreadable one is fatal
    // in exactly the way an unreadable header is: a partial entity set is a
    // state that silently lost records the resume cursor claims are present.
    chunks = []
    await forEachCursor(
      tx.objectStore(CHUNK_STORE).index(BY_OWNER_INDEX).openCursor(IDBKeyRange.only([userId, clientId])),
      cursor => chunks.push(cursor.value as CheckpointChunkRecord),
    )
  }
  catch {
    return { status: 'failed' }
  }

  // The op-log is a separate failure domain: it is a rebuildable tail over a
  // checkpoint that has already been read successfully, so a failure here
  // truncates the replay instead of discarding the base.
  try {
    const { frames, truncated, nextOrdinal } = await readOpLogFrames(tx.objectStore(OPLOG_STORE), userId, clientId)
    if (truncated)
      log.warn('op-log read stopped early; replaying the prefix and rewriting the checkpoint')
    return { status: 'ok', checkpoint, chunks, opLogFrames: frames, opLogTruncated: truncated, opLogNextOrdinal: nextOrdinal }
  }
  catch {
    log.warn('op-log unreadable; replaying nothing onto the checkpoint and rewriting it')
    return { status: 'ok', checkpoint, chunks, opLogFrames: [], opLogTruncated: true, opLogNextOrdinal: 0 }
  }
}

/** Frames read from an owner's op-log, plus whether the read stopped early. */
interface OpLogRead {
  frames: Uint8Array[]
  /** True when the read stopped early, so `frames` is a strict PREFIX. */
  truncated: boolean
  /** Ordinal the next appended segment must carry. See OpLogRecord.ordinal. */
  nextOrdinal: number
}

async function readOpLogFrames(store: IDBObjectStore, userId: string, clientId: string): Promise<OpLogRead> {
  // Cursor over the byOwner index for this (user, client). The index yields
  // equal-key records in primary-key (seq) order = append order.
  //
  // The walk STOPS at the first segment that breaks the read, rather than
  // collecting every segment and deciding afterwards: the caps exist to bound
  // memory on a cold start, and a decision taken after the flatten has already
  // paid for what it was meant to refuse.
  const frames: Uint8Array[] = []
  let bytes = 0
  let truncated = false
  let nextOrdinal = 0
  await forEachCursorWhile(
    store.index(BY_OWNER_INDEX).openCursor(IDBKeyRange.only([userId, clientId])),
    (cursor) => {
      const seg = cursor.value as OpLogRecord
      // STOP at the first ordinal that is not the one expected next. This is the
      // hole check, and it is the only one that catches an append that never
      // landed: a swallowed write failure, or two tabs that ended up sharing one
      // clientId and interleaved segments into a single owner's log. Everything
      // below is built on a strict PREFIX, so stopping here (rather than
      // skipping the row) keeps the contract and routes the bad tail to the
      // caller's rewrite.
      if (seg?.ordinal !== nextOrdinal) {
        truncated = true
        return false
      }
      // STOP at a malformed row -- do not skip it and keep reading. Everything
      // downstream is built on a strict-PREFIX contract: hydrate replays the
      // frames in order and the `batchEnd` frames among them advance the resume
      // cursor, so returning frames from BOTH sides of a gap would push the
      // cursor past the ops in the hole. The hub ships only what is strictly
      // after the cursor, so those ops would never be re-sent -- the same
      // silent, permanent divergence the per-(user, client) re-key above exists
      // to prevent, reached from the read side. A prefix plus `truncated`
      // routes to the caller's rewrite, which drops the bad tail from disk.
      if (!Array.isArray(seg?.framesBytes)) {
        truncated = true
        return false
      }
      for (const frame of seg.framesBytes) {
        // A segment may straddle a cap, so the cut is per FRAME: a prefix of a
        // segment is still a prefix of the log.
        const size = frame?.byteLength ?? 0
        if (frames.length >= MAX_OPLOG_READ_FRAMES || bytes + size > MAX_OPLOG_READ_BYTES) {
          truncated = true
          return false
        }
        frames.push(frame)
        bytes += size
      }
      // Advanced only for a segment accepted WHOLE. A segment cut short by a cap
      // is a prefix, and the caller rewrites rather than appending after it, so
      // claiming its ordinal was consumed would be wrong.
      nextOrdinal++
      return true
    },
  )
  return { frames, truncated, nextOrdinal }
}

/**
 * Append a batch of confirmed frames to this owner's op-log as ONE segment row.
 * Mirrors the backend's one-row-per-batch: a flush of N frames writes 1 row
 * holding all N, not N rows.
 *
 * `ordinal` is this segment's per-owner position and MUST come from a counter
 * the caller advances per attempted flush (see OpLogRecord.ordinal).
 *
 * Best-effort, and a failure still drops silently -- but it is no longer
 * SILENT-AND-INVISIBLE. A segment that does not land leaves a gap in the
 * ordinal sequence, and `readOpLogFrames` stops there, so the next cold start
 * replays the prefix up to the hole and rewrites. That is what makes the old
 * claim ("a missed segment only means a slightly staler log, still correct")
 * true: it held for a TRAILING segment and was false for a middle one, whose
 * post-hole `batchEnd` frames advanced the resume cursor past ops the hub would
 * never re-ship.
 */
export async function appendOpLogSegment(userId: string, clientId: string, framesBytes: Uint8Array[], ordinal: number): Promise<void> {
  if (!isCheckpointStoreAvailable() || framesBytes.length === 0)
    return
  try {
    const db = await openDb()
    const tx = db.transaction(OPLOG_STORE, 'readwrite')
    // `add`, not `put`: the key is generator-assigned, so a duplicate is
    // impossible by construction and an accidental overwrite should fail loud.
    tx.objectStore(OPLOG_STORE).add({ userId, clientId, ordinal, framesBytes } satisfies OpLogRecord)
    await txToPromise(tx, 'appendOpLogSegment')
  }
  catch {
    // Quota/private-browsing failures: persistence is an optimization only.
  }
}

/**
 * Atomically apply `delta` to this owner's checkpoint AND truncate its op-log.
 *
 * Every write shares ONE transaction, which is what keeps the pair consistent
 * in both directions: after this resolves the header, the chunks and the empty
 * op-log all describe the same instant, and a crash part-way rolls the whole
 * thing back rather than leaving the metadata row's watermark ahead of the
 * chunks it claims to describe. Mirrors the hub's CompactBatch.
 *
 * Best-effort; failures drop silently and return false (a failed compaction
 * only means the next cold reload replays a longer log, still correct) — and
 * the caller MUST keep its dirty set on a false, since none of the delta
 * landed.
 */
export async function writeCheckpointAndTruncateOpLog(
  userId: string,
  clientId: string,
  delta: CheckpointDelta,
  watermark: HlcShape,
  currentEpoch: bigint,
  now = Date.now(),
): Promise<boolean> {
  if (!isCheckpointStoreAvailable())
    return false
  try {
    const db = await openDb()
    const tx = db.transaction([CHECKPOINT_STORE, CHUNK_STORE, OPLOG_STORE], 'readwrite')
    // Registered in the same task that created the transaction, per txToPromise.
    const settled = txToPromise(tx, 'writeCheckpoint')
    const writes = (async () => {
      const chunks = tx.objectStore(CHUNK_STORE)
      const owner: [string, string] = [userId, clientId]
      if (delta.full) {
        // AWAITED before the upserts below. A cursor sees writes made in its
        // own transaction, so a chunk `put` issued while this walk is still
        // advancing would be deleted again the moment the cursor reached its
        // key. Awaiting a request of THIS transaction keeps it active across
        // the microtask checkpoint (see readCheckpoint).
        await deleteIndexRange(chunks, BY_OWNER_INDEX, IDBKeyRange.only(owner))
      }
      for (const chunk of delta.upserts) {
        chunks.put({
          userId,
          clientId,
          kind: chunk.kind,
          entityId: chunk.entityId,
          bytes: chunk.bytes,
        } satisfies CheckpointChunkRecord)
      }
      // Keyed deletes: the owner tuple is part of the primary key, so an
      // entity that left the state needs no index walk.
      for (const ref of delta.deletes)
        chunks.delete([userId, clientId, ref.kind, ref.entityId])
      tx.objectStore(CHECKPOINT_STORE).put({
        userId,
        clientId,
        headerBytes: delta.headerBytes,
        watermark,
        currentEpoch,
        writtenAt: now,
        lastSeenAt: now,
      } satisfies CheckpointRecord)
      // Truncate only THIS owner's log. The walk's own promise settles when it
      // has issued every delete; `settled` waits for durability, and a cursor
      // error aborts the transaction, which rejects it.
      await deleteIndexRange(tx.objectStore(OPLOG_STORE), BY_OWNER_INDEX, IDBKeyRange.only(owner))
    })()
    // Both are passed to Promise.all so neither can reject unobserved when the
    // other fails first.
    await Promise.all([writes, settled])
    return true
  }
  catch {
    log.warn('checkpoint write failed (quota exceeded / private mode); cold reloads will replay the existing op-log')
    return false
  }
}

/**
 * Refresh this owner's `lastSeenAt`, so the sweep can tell a quiet-but-running
 * tab from an abandoned one.
 *
 * A read-modify-write of the ONE metadata row, in a single transaction: it must
 * not resurrect a row the sweep just collected, and it must not clobber the
 * header/watermark a concurrent rewrite wrote, so it re-reads the record and
 * puts back exactly what it found with one field moved. A missing row is a
 * no-op -- an owner with no checkpoint has nothing for the sweep to protect,
 * and `keepClientId` already covers the sweeping tab itself.
 *
 * Best-effort and no-throw, like every operation in this module.
 */
export async function touchOwner(userId: string, clientId: string, now = Date.now()): Promise<boolean> {
  if (!isCheckpointStoreAvailable() || !userId || !clientId)
    return false
  try {
    const db = await openDb()
    const tx = db.transaction(CHECKPOINT_STORE, 'readwrite')
    const settled = txToPromise(tx, 'touchOwner')
    const store = tx.objectStore(CHECKPOINT_STORE)
    const existing = await requestToPromise<CheckpointRecord | undefined>(
      store.get([userId, clientId]) as IDBRequest<CheckpointRecord | undefined>,
    )
    if (existing === undefined) {
      // Nothing to touch. Let the transaction finish rather than abandoning it.
      await settled
      return false
    }
    store.put({ ...existing, lastSeenAt: now } satisfies CheckpointRecord)
    await settled
    return true
  }
  catch {
    return false
  }
}

/** Which rows a clear covers. See the two exported wrappers below. */
type ClearScope
  = | { kind: 'user', userId: string }
    | { kind: 'owner', userId: string, clientId: string }

/**
 * Delete the checkpoint metadata, chunk and op-log rows in `scope`, in one
 * transaction.
 *
 * Shared by both wrappers so they cannot drift on the transaction mechanics;
 * the scope is the entire difference between them. Best-effort: a failed clear
 * only leaves a stale record that will fail to parse on the next reload and
 * trigger another wipe attempt.
 */
async function clearScope(scope: ClearScope): Promise<boolean> {
  if (!isCheckpointStoreAvailable())
    return false
  try {
    const db = await openDb()
    const tx = db.transaction([CHECKPOINT_STORE, CHUNK_STORE, OPLOG_STORE], 'readwrite')
    const settled = txToPromise(tx, 'clearCheckpoint')
    const checkpoints = tx.objectStore(CHECKPOINT_STORE)
    const chunks = tx.objectStore(CHUNK_STORE)
    const opLog = tx.objectStore(OPLOG_STORE)
    // The checkpoint store's PRIMARY KEY is the owner tuple, so an owner-scoped
    // metadata clear is a single keyed delete and needs no index. The chunk and
    // op-log keys carry more than the owner (kind + entity id; a
    // generator-assigned `seq`), so both always walk an index.
    let walks: Promise<unknown>
    if (scope.kind === 'owner') {
      const owner: [string, string] = [scope.userId, scope.clientId]
      checkpoints.delete(owner)
      walks = Promise.all([
        deleteIndexRange(chunks, BY_OWNER_INDEX, IDBKeyRange.only(owner)),
        deleteIndexRange(opLog, BY_OWNER_INDEX, IDBKeyRange.only(owner)),
      ])
    }
    else {
      const range = IDBKeyRange.only(scope.userId)
      walks = Promise.all([
        forEachCursor(
          checkpoints.index(BY_USER_INDEX).openKeyCursor(range),
          cursor => checkpoints.delete(cursor.primaryKey),
        ),
        deleteIndexRange(chunks, BY_USER_INDEX, range),
        deleteIndexRange(opLog, BY_USER_INDEX, range),
      ])
    }
    await Promise.all([walks, settled])
    return true
  }
  catch {
    // Still swallowed -- a wipe is best-effort and no caller can do better than
    // carry on. But it now REPORTS, because "the wipe happened" and "the wipe
    // silently did not" are not interchangeable: hydrate.ts's corruption policy
    // is only self-healing if the poison record is actually gone, and a
    // versionchange from a peer tab or a quota abort on the readwrite tx makes
    // it not gone. Without a return value that is indistinguishable from
    // success, and the user pays a failed read plus a full projection snapshot
    // on every reload forever with nothing logged.
    return false
  }
}

/**
 * Delete the checkpoint, chunks and op-log for EVERY client of `userId`. Used
 * on logout / user switch, where the promise is "forget this device's state for
 * this account" — so it must span the other tabs' rows too, not just this one's.
 *
 * NOT for corruption recovery: a blob that failed to parse belongs to exactly
 * one `(userId, clientId)` owner, and wiping user-wide there would destroy
 * every OTHER tab's checkpoint too — a cross-tab clobber inflicted by the
 * recovery path itself, on tabs whose own records are fine. Use
 * `clearOwnerCheckpointAndOpLog` for that.
 */
export function clearCheckpointAndOpLog(userId: string): Promise<boolean> {
  return clearScope({ kind: 'user', userId })
}

/**
 * Delete the checkpoint, chunks and op-log for ONE `(userId, clientId)` owner,
 * leaving every other tab's rows untouched.
 *
 * This is the corruption-recovery scope. A record that fails to parse, names a
 * foreign tenant, or carries an out-of-range watermark/epoch is this owner's
 * problem alone; wiping it self-heals the next reload of THIS tab without
 * forcing every other tab into a full-snapshot cold start.
 */
export function clearOwnerCheckpointAndOpLog(userId: string, clientId: string): Promise<boolean> {
  return clearScope({ kind: 'owner', userId, clientId })
}

/** One checkpoint row as the sweep sees it: identity plus recency, no payload. */
interface SweepCandidate {
  userId: string
  clientId: string
  /** `lastSeenAt`, named `at` so `selectSweepVictims` can consume it. */
  at: number
}

/** Options for `sweepAbandonedCheckpoints`; every one is a test seam. */
export interface SweepOptions {
  now?: number
  ttlMs?: number
  maxOwners?: number
  /**
   * How recently an owner must have been touched to count as a RUNNING tab.
   * Defaults to OWNER_LIVENESS_WINDOW_MS.
   */
  livenessWindowMs?: number
}

/**
 * Delete the checkpoints + chunks + op-logs of this device's ABANDONED owners:
 * any row not TOUCHED within `ttlMs`, plus `userId`'s oldest rows beyond
 * `maxOwners`. Returns how many owners were collected.
 *
 * Nothing else ever reclaims them. The client id lives in sessionStorage, so it
 * dies with the tab: every closed tab strands a `(userId, clientId)` row holding
 * a serialized `UserCrdtState` -- header plus one chunk per entity -- and its
 * op-log segments, unreachable by any later session and deleted only by a logout
 * many users never perform. Left alone the origin's quota is eventually
 * exhausted, `writeCheckpointAndTruncateOpLog` starts returning false, the
 * checkpoint stops being refreshed, and every refresh falls back to the full
 * projection snapshot #267 exists to remove — while the browser reports large
 * and growing site storage.
 *
 * TWO ARMS, TWO SCOPES.
 *
 * The TTL arm applies to EVERY row the walk visits, including other accounts'.
 * It has to: an account signed out of without an explicit logout leaves rows no
 * later session ever addresses, so scoping the TTL to the signed-in user meant
 * nothing at all reclaimed them. It is safe because CHECKPOINT_TTL_MS is
 * comfortably longer than the hub's op-retention window -- a checkpoint that
 * old could not delta-resume even if its tab came back.
 *
 * The CAP arm stays scoped to `userId`. "This account has more tabs than the
 * cap" is a statement about this account's rows and says nothing about how many
 * another account may keep.
 *
 * LIVENESS is read from `lastSeenAt`, which every running tab refreshes on a
 * short interval (see `touchOwner`), so a row touched within
 * OWNER_LIVENESS_WINDOW_MS belongs to a tab that is still running. Both arms
 * skip those.
 *
 * This replaced a BroadcastChannel roll-call, and the reason is worth keeping:
 * a roll-call asks a question a backgrounded, frozen or bfcached tab CANNOT
 * answer, so no timeout value made it correct -- a short one killed live tabs
 * and a long one outlived the page that started the sweep. A timestamp the live
 * tab wrote for itself has no such window. It also inverts the default in the
 * right direction for a destructive operation: a row is kept on POSITIVE proof
 * of recency rather than on the absence of a reply.
 *
 * This is the counterpart the sibling store on the same `~/lib/idb` scaffold
 * already had (`sweepArtifacts`, TTL + entry cap); both now share the selection
 * arithmetic itself (`selectSweepVictims`).
 *
 * Best-effort and no-throw, like every operation in this module.
 */
export async function sweepAbandonedCheckpoints(
  userId: string,
  keepClientId: string,
  opts: SweepOptions = {},
): Promise<number> {
  if (!isCheckpointStoreAvailable() || !userId)
    return 0
  const now = opts.now ?? Date.now()
  const ttlMs = opts.ttlMs ?? CHECKPOINT_TTL_MS
  const maxOwners = opts.maxOwners ?? CHECKPOINT_MAX_OWNERS
  try {
    const db = await openDb()
    // Key-only walk, UNRANGED. The index key is writtenAt and the primary key
    // is [userId, clientId], so this reads no header or chunk bytes at all --
    // and a compound [userId, writtenAt] index, which would let the walk skip
    // other accounts' rows, is deliberately NOT added: those rows are exactly
    // the ones nothing else can reach, so making them cheap to skip would make
    // them permanently unreclaimable. The population this walks is capped at
    // ~maxOwners per account by this very sweep.
    const rows: SweepCandidate[] = []
    const readTx = db.transaction(CHECKPOINT_STORE, 'readonly')
    await forEachCursor(
      readTx.objectStore(CHECKPOINT_STORE).index(BY_LAST_SEEN_INDEX).openKeyCursor(),
      (cursor) => {
        const [rowUser, rowClient] = cursor.primaryKey as [string, string]
        rows.push({ userId: rowUser, clientId: rowClient, at: cursor.key as number })
      },
    )
    if (rows.length === 0)
      return 0

    // A row touched within the liveness window belongs to a running tab. The
    // sweeping tab is live by definition even if it has not written a row yet.
    const livenessWindowMs = opts.livenessWindowMs ?? OWNER_LIVENESS_WINDOW_MS
    const isLive = (row: SweepCandidate): boolean =>
      row.clientId === keepClientId || now - row.at < livenessWindowMs
    // The index yields lastSeenAt-ascending, so both partitions stay oldest-first.
    const candidates = rows.filter(row => !isLive(row))
    const mine = candidates.filter(row => row.userId === userId)
    const foreign = candidates.filter(row => row.userId !== userId)
    // This tab occupies a slot in its own account's budget whether or not it
    // has written a row yet, and so does every live sibling that has one.
    const reserved = 1 + rows.filter(row => row.userId === userId
      && row.clientId !== keepClientId
      && isLive(row)).length
    const victims = [
      ...selectSweepVictims(foreign, { now, ttlMs }),
      ...selectSweepVictims(mine, { now, ttlMs, maxEntries: maxOwners, reserved }),
    ]
    if (victims.length === 0)
      return 0

    const tx = db.transaction([CHECKPOINT_STORE, CHUNK_STORE, OPLOG_STORE], 'readwrite')
    const settled = txToPromise(tx, 'sweepAbandonedCheckpoints')
    const checkpoints = tx.objectStore(CHECKPOINT_STORE)
    const chunks = tx.objectStore(CHUNK_STORE)
    const opLog = tx.objectStore(OPLOG_STORE)
    const walks = victims.flatMap((victim) => {
      // Keyed off the VICTIM's user, not the sweeping one: the TTL arm reaches
      // across accounts, so the closure's `userId` is the wrong key for a
      // foreign row and would delete some other owner entirely.
      const owner: [string, string] = [victim.userId, victim.clientId]
      checkpoints.delete(owner)
      return [
        deleteIndexRange(chunks, BY_OWNER_INDEX, IDBKeyRange.only(owner)),
        deleteIndexRange(opLog, BY_OWNER_INDEX, IDBKeyRange.only(owner)),
      ]
    })
    await Promise.all([Promise.all(walks), settled])
    return victims.length
  }
  catch {
    // Best-effort: a failed sweep just leaves the rows for the next attempt.
    return 0
  }
}

/**
 * Delete every row of `store` matching `range` on `indexName`, by walking the
 * index and deleting each primary key. Runs within the caller's readwrite
 * transaction; resolves once the walk has issued every delete (the caller
 * awaits tx.oncomplete for durability).
 *
 * Index-driven rather than a primary-key range because neither store this
 * serves is keyed by the owner alone: the op-log's key is the
 * generator-assigned `seq`, which carries no owner information at all, and a
 * chunk's key carries the owner plus a kind and an entity id.
 */
function deleteIndexRange(store: IDBObjectStore, indexName: string, range: IDBKeyRange): Promise<void> {
  return forEachCursor(
    store.index(indexName).openKeyCursor(range),
    cursor => store.delete(cursor.primaryKey),
  )
}
