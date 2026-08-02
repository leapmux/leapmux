import type { ChunkKind } from './checkpointChunks'
import type { HlcShape } from './hlc'
import type { UserCrdtState } from '~/generated/leapmux/v1/user_crdt_pb'
import type { WatchUserEvent } from '~/generated/leapmux/v1/user_ops_pb'
import { fromBinary } from '@bufbuild/protobuf'
import { WatchUserEventSchema } from '~/generated/leapmux/v1/user_ops_pb'
import { createLogger } from '~/lib/logger'
import { applyChunk, isChunkKind, parseHeader } from './checkpointChunks'
import { clearOwnerCheckpointAndOpLog, isCheckpointStoreAvailable, readCheckpoint } from './checkpointStore'
import { inInt64Range } from './hlc'

const log = createLogger('crdtHydrate')

// ---------------------------------------------------------------------------
// Hydration: load + validate a persisted checkpoint + op-log for cold reload
//
// This module owns the DESERIALIZATION-FAILURE POLICY in one place, and the
// policy differs by WHICH half failed:
//
//   - CHECKPOINT unusable (bad header blob, an undecodable or unknown-kind
//     entity chunk, foreign tenant, missing/out-of-range watermark or epoch) →
//     WIPE BOTH and return null. There is no base to replay onto, so the pair
//     is worthless as a unit. This self-heals (a corrupt record would otherwise
//     fail every subsequent refresh) and triggers the full-snapshot path —
//     empty manager → bootstrap() from UserMaterialized → first checkpoint
//     write rebuilds a known-good store.
//
//     The chunks belong to THIS arm and not the op-log's, even though there are
//     many of them and each parses independently: a partially-assembled state
//     is not an older state, it is a state silently missing records, and the
//     resume cursor rides on it. Partial is the one outcome that must stay
//     impossible.
//   - OP-LOG frame unusable → keep the checkpoint and replay the decodable
//     PREFIX, reporting `truncated` so the caller rewrites the checkpoint and
//     drops the bad tail. The log is an ordered cache OVER the checkpoint, so
//     one bad frame invalidates only what follows it; discarding the whole
//     base would force exactly the full snapshot this feature exists to avoid.
//
// The store (checkpointStore.ts) stays a pure blob store (returns raw
// Uint8Array, never deserializes), so this module and checkpointChunks.ts are
// the only places proto coupling lands and the parse/replay path is
// unit-testable without IDB.
// ---------------------------------------------------------------------------

/**
 * The validated hydration payload handed to PendingOpsManager.hydrate: the
 * parsed checkpoint state, the op-log frames to replay (in apply order), the
 * resume watermark pinned at checkpoint time, and the epoch. All proto objects
 * are already parsed and validated — hydrate() applies them without touching
 * IDB or proto codecs.
 */
export interface HydrationPayload {
  /** The full UserCrdtState at checkpoint time T_c (the replay base). */
  state: UserCrdtState
  /** Confirmed WatchUserEvent frames to replay onto `state` (T_c → T_now). */
  frames: WatchUserEvent[]
  /** The resume watermark pinned at checkpoint write time. */
  watermark: HlcShape
  /** The epoch the watermark was seen under. */
  currentEpoch: bigint
  /**
   * True when `frames` is a PREFIX of the persisted op-log — because a frame
   * failed to decode and the replay stopped there, or because the store's read
   * hit its own frame/byte ceiling. The hydrated state is consistent (just
   * older); the caller should rewrite the checkpoint at the post-replay
   * watermark so the rest is dropped from disk -- otherwise every reload
   * re-truncates at the same place.
   */
  truncated: boolean
  /**
   * Ordinal the next appended op-log segment must carry, so the recorder can
   * continue the persisted sequence instead of restarting at 0 behind segments
   * that already hold 0..N-1 — which the next cold start would read as a hole
   * and discard an intact log for. See `OpLogRecord.ordinal`.
   */
  nextOpLogOrdinal: number
}

/**
 * Wrap fromBinary in a never-throw parse: a corrupt or truncated blob returns
 * undefined instead of propagating the decode error. Callers treat undefined
 * as "unusable, wipe and fall back."
 */
function tryFromBinary<T>(decode: (bytes: Uint8Array) => T, bytes: Uint8Array): T | undefined {
  try {
    return decode(bytes)
  }
  catch {
    return undefined
  }
}

/**
 * Merge one entity chunk into `state`, reporting failure rather than throwing.
 * The sibling of `tryFromBinary` for the sharded half of the checkpoint.
 */
function tryApplyChunk(state: UserCrdtState, kind: ChunkKind, entityId: string, bytes: Uint8Array): boolean {
  try {
    applyChunk(state, kind, entityId, bytes)
    return true
  }
  catch {
    return false
  }
}

/**
 * Wipe this owner's persisted pair and report a cold start.
 *
 * OWNER-scoped, not user-scoped, and that is the whole point. Every failure
 * this module detects — an unreadable row, an undecodable blob, a foreign
 * tenant, an out-of-range watermark or epoch — belongs to exactly one
 * `(userId, clientId)` record. Wiping user-wide would delete every OTHER open
 * tab's checkpoint and op-log as collateral, forcing each of them to cold-start
 * with the full projection snapshot #267 exists to avoid — the cross-tab
 * clobber the per-owner key exists to make unlikely (it cannot make it
 * impossible; see checkpointStore's header on duplicated tabs).
 * `clearCheckpointAndOpLog` (user-wide) stays for logout / user switch, where
 * spanning every tab is the intent.
 */
async function wipeAndFail(userId: string, clientId: string): Promise<null> {
  // A wipe that did not happen leaves the poison record in place, so the next
  // reload reads it, fails identically, and pays a full projection snapshot --
  // forever, and silently. The wipe is still best-effort (there is nothing
  // better to do here), but "self-healing" is a claim this module makes in its
  // own header, and an unobservable failure makes that claim unfalsifiable.
  if (!await clearOwnerCheckpointAndOpLog(userId, clientId))
    log.warn('could not wipe the corrupt checkpoint; the next reload will re-read it', { clientId })
  return null
}

/**
 * Load and validate the persisted checkpoint + op-log for `userId`. Returns
 * null when there is nothing usable to hydrate from:
 *   - the store is unavailable (no indexedDB),
 *   - no checkpoint exists,
 *   - the checkpoint header blob fails to deserialize,
 *   - the header names a different tenant,
 *   - the watermark/epoch is missing or out of range,
 *   - an entity chunk names an unknown kind or fails to deserialize.
 *
 * Each of those wipes THIS OWNER's pair (see wipeAndFail) so the next reload
 * isn't poisoned by the same corrupt record, and returns null — routing the
 * caller to the full-snapshot cold-start path.
 *
 * An undecodable OP-LOG frame is NOT fatal: the returned payload carries the
 * decodable prefix with `truncated: true`, and the caller rewrites the
 * checkpoint to drop the bad tail.
 */
export async function loadHydrationState(userId: string, clientId: string): Promise<HydrationPayload | null> {
  if (!isCheckpointStoreAvailable() || !userId || !clientId)
    return null

  const read = await readCheckpoint(userId, clientId)
  if (read.status === 'miss')
    return null
  if (read.status === 'failed') {
    // Distinct from a miss on purpose. A checkpoint row that EXISTS but cannot
    // be read (a structured-clone failure on the stored bytes, a corrupt key
    // entry) would otherwise survive every reload: each one pays the failed
    // read, cold-starts, and re-fails identically. Wiping is what makes the
    // policy in this module's header self-healing rather than a loop.
    return wipeAndFail(userId, clientId)
  }
  const { checkpoint, chunks, opLogFrames, opLogTruncated } = read

  // (1) The checkpoint header blob must deserialize into a UserCrdtState.
  const state = tryFromBinary(parseHeader, checkpoint.headerBytes)
  if (!state) {
    return wipeAndFail(userId, clientId)
  }

  // (1b) The blob must name THIS tenant. UserCrdtState carries its own user_id,
  // so adopting it on the strength of the row key alone would let a persisted
  // record -- not the authenticated session -- decide whose workspaces, tiles
  // and tabs this shell renders. The wire path refuses a foreign-tenant
  // UserMaterialized for exactly this reason (see applyMaterialized, the client
  // half of crdt.Manager.requireOwnState); the cold-start path must fail closed
  // the same way. Wipe rather than merely skip: a row keyed under this user but
  // carrying another user's state is corrupt by construction.
  //
  // A MISSING user_id is corrupt too, and is checked by the same comparison
  // rather than short-circuited past it. Guarding with `state.userId &&` would
  // admit the one blob that names no tenant at all -- the only record here whose
  // provenance is completely unknown -- while every other validation in this
  // function (watermark, epoch, chunk kind) treats absent as fatal. No writer
  // produces one: `newState(userId)` seeds even the cold-start manager, a
  // bootstrap copies `materialized.userId`, and a checkpoint is only ever
  // written once a watermark exists, which requires one of those two.
  if (state.userId !== userId) {
    return wipeAndFail(userId, clientId)
  }

  // (2) The watermark and epoch must be present and in-range. An out-of-range
  // epoch would 400 at the hub on the resume URL, and a missing watermark
  // leaves no cursor to send — both degrade to a full snapshot, but a stale
  // record left in the store would re-fail every reload, so wipe it.
  const wm = checkpoint.watermark
  if (!wm || typeof wm.physical !== 'bigint' || typeof wm.logical !== 'bigint' || typeof wm.clientId !== 'string') {
    return wipeAndFail(userId, clientId)
  }
  // RANGE, not just type -- the comment above says "in-range" and the epoch two
  // lines down enforces it, but the watermark used to be type-checked only. An
  // out-of-range physical/logical survives here and then fails
  // `validateResumeHlc` at every socket open, so the client silently takes a
  // full snapshot on every connect instead of resuming. It self-heals after one
  // bootstrap (which overwrites the watermark and rewrites the pair), but a
  // wipe here turns one silent degraded connect into zero.
  if (!inInt64Range(wm.physical) || !inInt64Range(wm.logical)) {
    return wipeAndFail(userId, clientId)
  }
  const epoch = checkpoint.currentEpoch
  if (typeof epoch !== 'bigint' || !inInt64Range(epoch)) {
    return wipeAndFail(userId, clientId)
  }

  // (2b) Re-assemble the entity maps from the chunks. EVERY chunk must land:
  // one that names a kind this build does not know, or that fails to decode,
  // yields a state missing exactly that record while the header's watermark
  // still claims to describe it -- and the resume cursor would then be sent
  // for a base that is silently short. That is worse than no checkpoint at
  // all, so it takes the same wipe-and-cold-start arm as a bad header.
  for (const chunk of chunks) {
    if (!isChunkKind(chunk.kind))
      return wipeAndFail(userId, clientId)
    const applied = tryApplyChunk(state, chunk.kind, chunk.entityId, chunk.bytes)
    if (!applied)
      return wipeAndFail(userId, clientId)
  }

  // (3) Replay the op-log as far as it decodes. A bad frame invalidates only
  // the frames AFTER it: applying a strict PREFIX is coherent because hydrate()
  // advances the watermark per applied frame, so stopping at frame i leaves
  // exactly (state = T_c + frames[0..i), watermark = max over those) -- an
  // older cursor, but one that truthfully describes the state it labels, which
  // is all the hub's delta path needs.
  //
  // Wiping the pair here instead would throw away a checkpoint that steps (1)
  // and (2) just proved valid, forcing the full-snapshot projection scan that
  // issue #267 exists to avoid -- in the one case where a cheap recovery
  // matters most. The caller drops the undecodable tail from disk (see
  // `truncated`).
  const frames: WatchUserEvent[] = []
  // An op-log the store could only partially read is already a prefix, so it
  // carries the same "rewrite the checkpoint to drop the rest" obligation as an
  // undecodable frame. The frame and byte CEILINGS live at the store's cursor
  // (MAX_OPLOG_READ_FRAMES / MAX_OPLOG_READ_BYTES, which mirror the hub's
  // MaxResumeDeltaOps / MaxResumeDeltaBytes) rather than here: a bound checked
  // after the whole log has been flattened into memory has already paid the
  // cost it exists to refuse.
  let truncated = opLogTruncated
  for (const frameBytes of opLogFrames) {
    const frame = tryFromBinary(bytes => fromBinary(WatchUserEventSchema, bytes), frameBytes)
    if (!frame) {
      truncated = true
      break
    }
    frames.push(frame)
  }

  return { state, frames, watermark: wm, currentEpoch: epoch, truncated, nextOpLogOrdinal: read.opLogNextOrdinal }
}
