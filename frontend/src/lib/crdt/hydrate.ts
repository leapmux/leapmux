import type { ChunkKind } from './checkpointChunks'
import type { CheckpointRead } from './checkpointStore'
import type { HlcShape } from './hlc'
import type { UserCrdtState } from '~/generated/proto/leapmux/v1/user_crdt_pb'
import type { WatchUserEvent } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { fromBinary } from '@bufbuild/protobuf'
import { WatchUserEventSchema } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { createLogger } from '~/lib/logger'
import { applyChunk, isChunkKind, parseHeader } from './checkpointChunks'
import {
  adoptCheckpoint,
  clearOwnerCheckpointAndOpLog,
  isCheckpointStoreAvailable,
  listSeedCandidates,
  readCheckpoint,
} from './checkpointStore'
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
   * The persisted base this payload sits on: a value when it is already durable
   * under this owner's key, a PROMISE of one while it is still being written,
   * or null when there is none. Present (eventually) means the recorder may
   * rewrite INCREMENTALLY against it.
   *
   * Already settled on the self-hydration path — the caller read those bytes
   * back off disk. A promise on the SEED path, where the adoption is left in
   * flight on purpose so the caller can open its ready gate (and so the
   * /ws/userevents connect and first render) without waiting out a write of one
   * row per entity. It resolves null when the adoption did NOT commit: the
   * state and frames are still usable in memory, so the tab still presents a
   * cursor and RESUMEs, but the recorder must then be given no base at all. An
   * incremental rewrite over chunks that are not there writes a header plus a
   * handful of shards and nothing underneath, which the next cold start
   * hydrates as a state silently missing almost every record -- with the resume
   * cursor riding on it. That is the one outcome hydrate's corruption policy
   * exists to make impossible, reached from the write side.
   *
   * NESTED rather than a `basePersisted` boolean beside a flat
   * `nextOpLogOrdinal`: the two are one fact, and split apart the type could
   * express "no base on disk, but resume the log at ordinal 7" -- a producer
   * that cleared the flag and forgot to zero the ordinal. `frames` stays
   * top-level because it is consumed unconditionally (the in-memory replay
   * happens whether or not anything was persisted).
   */
  persistedBase: PersistedBase | null | Promise<PersistedBase | null>
}

/** Where the recorder must continue the persisted op-log sequence. */
export interface PersistedBase {
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
 * Wipe this owner's persisted pair.
 *
 * It reports NOTHING about what to do next. It used to return `Promise<null>`
 * and be named `wipeAndFail`, because a wipe WAS the cold-start decision at all
 * eight call sites that returned its result directly. It no longer is: the sole
 * caller now falls through to the sibling seed, and a name that still advertises
 * the old control flow invites the next failure arm to write
 * `return wipeAndFail(...)` and silently reinstate the unconditional cold start
 * the seed path exists to remove.
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
async function wipeOwnCheckpoint(userId: string, clientId: string): Promise<void> {
  // A wipe that did not happen leaves the poison record in place, so the next
  // reload reads it, fails identically, and pays a full projection snapshot --
  // forever, and silently. The wipe is still best-effort (there is nothing
  // better to do here), but "self-healing" is a claim this module makes in its
  // own header, and an unobservable failure makes that claim unfalsifiable.
  if (!await clearOwnerCheckpointAndOpLog(userId, clientId))
    log.warn('could not wipe the corrupt checkpoint; the next reload will re-read it', { clientId })
}

/**
 * Load and validate the persisted checkpoint + op-log for `userId`.
 *
 * It tries THIS OWNER first, and falls through to a SIBLING owner when this one
 * has nothing usable -- either because there is no row at all (a genuinely new
 * tab, the first tab after a browser restart, a re-minted duplicate), or
 * because this owner's row failed validation:
 *   - the checkpoint header blob fails to deserialize,
 *   - the header names a different tenant,
 *   - the watermark/epoch is missing or out of range,
 *   - an entity chunk names an unknown kind or fails to deserialize.
 *
 * Each of THOSE wipes THIS OWNER's pair (see wipeOwnCheckpoint) so the next
 * reload isn't poisoned by the same corrupt record. The wipe and the seed are separate
 * decisions: the wipe is about our own bad row, the seed about finding a good
 * one. See seedFromSibling, which never deletes a row it does not own.
 *
 * Returns null -- routing the caller to the full-snapshot cold-start path --
 * only when the store is unavailable, the ids are empty, or no sibling
 * qualifies either.
 *
 * An undecodable OP-LOG frame is NOT fatal: the returned payload carries the
 * decodable prefix with `truncated: true`, and the caller rewrites the
 * checkpoint to drop the bad tail.
 */
export async function loadHydrationState(
  userId: string,
  clientId: string,
  opts: HydrationOptions = {},
): Promise<HydrationPayload | null> {
  if (!isCheckpointStoreAvailable() || !userId || !clientId)
    return null

  const read = await readCheckpoint(userId, clientId)
  if (read.status === 'miss')
    return seedFromSibling(userId, clientId, opts)
  const payload = validateCheckpoint(userId, read)
  if (payload)
    return payload
  // Our own record is unusable. Wipe it so the next reload is not poisoned by
  // the same corruption, then try a sibling: recovering from OUR OWN bad
  // checkpoint is one more case the seed path covers, and it is strictly better
  // than the full projection snapshot this used to mean.
  //
  // Checked HERE too, not only before the seed's adoption: the wipe is the
  // OTHER write on this path, and the more destructive one. A run that lost the
  // caller's deadline has already cold-started, bootstrapped, and had its
  // recorder write a fresh checkpoint under this same owner key with
  // `base = 'ready'` -- so a late wipe deletes those rows while the recorder
  // goes on rewriting INCREMENTALLY against chunks that are no longer there,
  // landing a header plus a handful of shards over nothing. That is this
  // module's own "partial is the one outcome that must stay impossible",
  // reached from the write side.
  if (opts.superseded?.())
    return null
  await wipeOwnCheckpoint(userId, clientId)
  return seedFromSibling(userId, clientId, opts)
}

/**
 * Validate an already-read checkpoint into a HydrationPayload, or null when the
 * pair is unusable.
 *
 * PURE and side-effect-free: it decides only WHETHER the pair is usable, never
 * what to do about it. The recovery differs by owner and so cannot live here --
 * wiping is correct for YOUR OWN row and catastrophic for a sibling's, whose
 * recorder believes its chunks are on disk and rewrites INCREMENTALLY against
 * them. Being pure also means one definition of the policy serves both entry
 * points, which is the whole reason the seed path can be trusted to reject
 * exactly what the self path rejects.
 *
 * Nullable rather than a three-arm 'miss' | 'corrupt' | 'ok' union: no caller
 * ever distinguished the first two. loadHydrationState has already returned on
 * a miss before it gets here, and the seed skips the candidate either way -- so
 * the discriminant was an arm every reader had to check was handled and then
 * find was not.
 */
function validateCheckpoint(userId: string, read: CheckpointRead): HydrationPayload | null {
  // A 'failed' read is NOT the same as a miss to the CALLER, which wipes on the
  // former: a checkpoint row that EXISTS but cannot be read (a structured-clone
  // failure on the stored bytes, a corrupt key entry) would otherwise survive
  // every reload, each one paying the failed read, cold-starting, and re-failing
  // identically. Wiping is what makes the policy in this module's header
  // self-healing rather than a loop. Both are simply "unusable" HERE.
  if (read.status !== 'ok')
    return null
  const { checkpoint, chunks, opLogFrames, opLogTruncated } = read

  // (1) The checkpoint header blob must deserialize into a UserCrdtState.
  const state = tryFromBinary(parseHeader, checkpoint.headerBytes)
  if (!state) {
    return null
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
    return null
  }

  // (2) The watermark and epoch must be present and in-range. An out-of-range
  // epoch would 400 at the hub on the resume URL, and a missing watermark
  // leaves no cursor to send — both degrade to a full snapshot, but a stale
  // record left in the store would re-fail every reload, so wipe it.
  const wm = checkpoint.watermark
  if (!wm || typeof wm.physical !== 'bigint' || typeof wm.logical !== 'bigint' || typeof wm.clientId !== 'string') {
    return null
  }
  // RANGE, not just type -- the comment above says "in-range" and the epoch two
  // lines down enforces it, but the watermark used to be type-checked only. An
  // out-of-range physical/logical survives here and then fails
  // `validateResumeHlc` at every socket open, so the client silently takes a
  // full snapshot on every connect instead of resuming. It self-heals after one
  // bootstrap (which overwrites the watermark and rewrites the pair), but a
  // wipe here turns one silent degraded connect into zero.
  if (!inInt64Range(wm.physical) || !inInt64Range(wm.logical)) {
    return null
  }
  const epoch = checkpoint.currentEpoch
  if (typeof epoch !== 'bigint' || !inInt64Range(epoch)) {
    return null
  }

  // (2b) Re-assemble the entity maps from the chunks. EVERY chunk must land:
  // one that names a kind this build does not know, or that fails to decode,
  // yields a state missing exactly that record while the header's watermark
  // still claims to describe it -- and the resume cursor would then be sent
  // for a base that is silently short. That is worse than no checkpoint at
  // all, so it takes the same wipe-and-cold-start arm as a bad header.
  for (const chunk of chunks) {
    if (!isChunkKind(chunk.kind))
      return null
    const applied = tryApplyChunk(state, chunk.kind, chunk.entityId, chunk.bytes)
    if (!applied)
      return null
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

  return {
    state,
    frames,
    watermark: wm,
    currentEpoch: epoch,
    truncated,
    // Present by definition on this path: the caller read these bytes back OUT
    // of this owner's own rows, so they are on disk under this owner's key.
    // The seed path overrides it.
    persistedBase: { nextOpLogOrdinal: read.opLogNextOrdinal },
  }
}

/** Options for `loadHydrationState`; every one is a test seam or a guard. */
export interface HydrationOptions {
  /**
   * True once this hydration run has been SUPERSEDED -- its effect re-ran, its
   * owner was disposed, or the caller's deadline elapsed.
   *
   * Checked immediately before the seed's adoption, which is the only WRITE on
   * this path. useCrdtRuntime races the read against a deadline and, on losing,
   * cold-starts WITHOUT cancelling the promise behind it -- harmless while this
   * was a pure read, and fatal the moment it is not. The sequence to prevent:
   * deadline fires, the tab cold-starts, bootstrap() rewrites the checkpoint,
   * the recorder begins appending ordinals 0,1,2..., and only THEN the seed's
   * adoption lands -- replacing the fresh checkpoint with the sibling's older
   * header and the log with one segment at ordinal 0. Disk then holds a hole
   * whose replayed batchEnd frames advance the resume cursor straight past it,
   * and the hub ships only what is strictly after the cursor. Silent, permanent
   * divergence.
   */
  superseded?: () => boolean
  /**
   * Test seam: how many candidates to try before giving up.
   *
   * Named to match `SeedCandidateOptions.limit`, which it is forwarded to
   * verbatim one call later -- one knob should not have two names a single hop
   * apart. (A sibling `now`/`maxAgeMs` pair was declared here too and deleted:
   * nothing ever set `now`, and `maxAgeMs` was never forwarded at all, so the
   * age cutoff was unreachable through this entry point. `listSeedCandidates`
   * takes both directly and is tested through them.)
   */
  limit?: number
}

/**
 * Seed this tab from a SIBLING tab's persisted checkpoint.
 *
 * Checkpoints are per-(user, client) because of a WRITE clobber (see
 * checkpointStore's header); a READER has no such hazard, so a tab with nothing
 * usable of its own -- a genuinely new tab, the first tab after a browser
 * restart, a duplicate whose id was re-minted, or one whose own checkpoint just
 * failed validation -- may adopt a sibling's base and RESUME instead of paying a
 * full projection snapshot.
 *
 * IT NEVER DELETES ANYTHING. A corrupt candidate is SKIPPED, not wiped, even
 * one that looks abandoned. Wiping a LIVE sibling is catastrophic in a way the
 * self path is not: that tab's recorder believes its chunks are on disk and
 * rewrites INCREMENTALLY against them, so removing them leaves it writing a
 * header plus a few shards over nothing -- the "partial is the one outcome that
 * must stay impossible" hazard in this module's header, inflicted on a tab
 * whose own records were fine. And "abandoned" is not provable here:
 * OWNER_LIVENESS_WINDOW_MS is a heuristic a frozen or bfcached page fails.
 * sweepAbandonedCheckpoints already owns reclamation, and one destructive
 * policy over rows we do not own is enough.
 */
async function seedFromSibling(
  userId: string,
  clientId: string,
  opts: HydrationOptions,
): Promise<HydrationPayload | null> {
  const candidates = await listSeedCandidates(userId, clientId, { limit: opts.limit })
  for (const candidate of candidates) {
    const read = await readCheckpoint(userId, candidate.clientId)
    // Narrow ONCE, here, so everything below reads the candidate's bytes off a
    // `read` the type system has already proven to be present.
    //
    // The alternative -- re-testing `read.status === 'ok'` at each use with a
    // fallback -- looked defensive and was the opposite: the fallbacks
    // (`new Uint8Array()`, `[]`) are unreachable today, and the only behaviour
    // they COULD ever produce is adopting an empty header with zero chunks
    // under our own key, which adoptCheckpoint's full-replace write would make
    // durable. That is precisely "a state silently missing almost every record,
    // with the resume cursor riding on it" -- the one outcome this module's
    // header calls impossible.
    if (read.status !== 'ok')
      continue
    const payload = validateCheckpoint(userId, read)
    if (!payload)
      continue
    // Checked HERE, and again inside adoptCheckpoint immediately before its
    // transaction is created.
    //
    // One check would be sufficient IF the adopt transaction were always
    // CREATED in this same task -- everything that could supersede us
    // (withTimeout's deadline, Solid's cleanup) runs in a LATER one, and
    // IndexedDB starts overlapping-scope readwrite transactions in creation
    // order, so an adoption created here always commits before any write the
    // resulting cold start issues, and that cold start's full rewrite lands on
    // top of it rather than interleaving with it.
    //
    // But adoptCheckpoint's `await openDb()` is a no-op microtask only while
    // the cached connection is live. A peer tab's schema-repair
    // `deleteDatabase()` nulls it (idb.ts's `onversionchange` handler), and the
    // reopen that follows is a whole task -- which is exactly long enough for
    // the ordering argument to lapse. So the guard is re-consulted at the point
    // the transaction is actually created, where it needs no ordering argument
    // at all.
    if (opts.superseded?.()) {
      // Write NOTHING, and tell the caller its recorder has no base on disk.
      //
      // The payload is still returned rather than null because it is correct in
      // memory and costs nothing to hand back. No CURRENT caller observes it --
      // withTimeout has already resolved null on the deadline path, and
      // hydrateAndInstall returns at its `cancelled` guard on the other -- so
      // this is the contract, not a live behaviour: whoever does consume it gets
      // a usable state with no base, which is exactly what a failed adoption
      // reports below.
      return { ...payload, persistedBase: null }
    }
    // NOT awaited. The adoption is one IDB `put` per entity, and nothing the
    // caller does next needs it: the state and frames above are already in
    // memory, so the ready gate -- and with it the /ws/userevents connect, the
    // resume cursor and the first render -- can open while this write is still
    // committing. Only the RECORDER needs the result, and it holds its appends
    // and rewrites until this settles (see CheckpointRecorderOptions).
    //
    // Copy the VALIDATED PREFIX, not everything the read returned.
    //
    // `payload.frames` is where validateCheckpoint stopped -- it decodes one raw
    // frame per entry, in order, and breaks on the first failure -- so
    // `read.opLogFrames` beyond that length is either undecodable or past the
    // store's own read ceiling. Copying it would make this owner's row a second
    // durable copy of the source's corruption, and the one-shot repair the
    // caller asks for (`rewriteNow` on `truncated`) is issued while the recorder
    // is still holding for this very adoption.
    //
    // Taking the prefix instead makes the adopted pair self-consistent by
    // construction: the frames on disk are exactly the frames that produced the
    // watermark in the header beside them, so `opLogCount` matches the row and
    // the read ceiling is armed against the true length.
    const adoptedFrames = read.opLogFrames.slice(0, payload.frames.length)
    // The ordinal comes back FROM the adoption rather than being re-derived
    // here: it is a fact about the segment layout adoptCheckpoint chose, and a
    // producer and a consumer that disagree by one mint exactly the hole the
    // per-owner sequence exists to detect. Nesting it under the base is what
    // removes the second `adopted &&` guard this expression used to need -- an
    // ordinal cannot outlive the base it counts.
    const persistedBase = adoptCheckpoint(
      userId,
      clientId,
      {
        headerBytes: read.checkpoint.headerBytes,
        chunks: read.chunks,
        watermark: payload.watermark,
        currentEpoch: payload.currentEpoch,
        opLogFrames: adoptedFrames,
      },
      undefined,
      opts.superseded,
    ).then((nextOpLogOrdinal) => {
      log.info('seeded this tab from a sibling checkpoint', {
        from: candidate.clientId,
        adopted: nextOpLogOrdinal !== null,
      })
      return nextOpLogOrdinal === null ? null : { nextOpLogOrdinal }
    })
    return { ...payload, persistedBase }
  }
  return null
}
