import type { DescMessage, Message } from '@bufbuild/protobuf'
import type { CheckpointDelta } from './checkpointStore'
import type { OpEntityKind } from './ops'
import type { UserCrdtState } from '~/generated/leapmux/v1/user_crdt_pb'
import type { CrdtOp, EntityMaterialized, EntityRemoved, WatchUserEvent } from '~/generated/leapmux/v1/user_ops_pb'
import { fromBinary, toBinary } from '@bufbuild/protobuf'
import {
  FloatingWindowRecordSchema,
  NodeRecordSchema,
  TabRecordSchema,
  UserCrdtStateSchema,
  WorkspaceContentsRecordSchema,
} from '~/generated/leapmux/v1/user_crdt_pb'
import { opTarget } from './ops'

// ---------------------------------------------------------------------------
// Sharding UserCrdtState into a header + one chunk per entity
//
// The checkpoint used to be ONE blob: `toBinary(UserCrdtStateSchema,
// confirmedState)` over the whole account, re-run on the main thread every 256
// confirmed frames. That cost scales with the ACCOUNT, while the delta that
// triggered it is 256 ops on one or two entities -- measured at 7.1 ms for 400
// nodes / 600 tabs and 56.7 ms for 2400 / 4800, landing mid-drag.
//
// So the persisted checkpoint is split:
//
//   - the HEADER: the state with every entity map emptied. Scalars only
//     (user id, max/compaction/op-retention HLCs, epoch), so it is O(1) in
//     account size and cheap to rewrite on every checkpoint.
//   - one CHUNK per entity, `(kind, entityId) -> serialized record`. A rewrite
//     re-serializes only the entities that actually changed.
//
// The measured effect is that the AFTER numbers stop tracking account size at
// all: header alone is 0.004 ms and header + 2 dirty chunks 0.015 ms at EVERY
// size benchmarked (5/5 through 2400/4800), against 0.111 ms / 56.7 ms for the
// whole blob over the same range. The full rewrite is still O(account) --
// 56.7 ms at 2400/4800 -- which is why it is reserved for a bootstrap, where
// the state was just replaced wholesale anyway.
//
// This module owns the split and its inverse, and is the ONLY place that knows
// which record schema each kind carries. The recorder serializes through it,
// hydrate reassembles through it, and checkpointStore stays a pure blob store
// that treats `kind` / `entityId` / `bytes` as opaque.
// ---------------------------------------------------------------------------

/**
 * The map fields of `UserCrdtState` — everything sharded into chunks.
 *
 * Derived from the generated type rather than listed by hand, so the
 * exhaustiveness assertion below is checked against the proto itself.
 */
type ShardedField = {
  [K in keyof UserCrdtState]-?: UserCrdtState[K] extends Record<string, Message> ? K : never
}[keyof UserCrdtState]

/** One kind's wiring: which map it lives in and which schema its records use. */
interface ShardSpec {
  field: ShardedField
  schema: DescMessage
  /** Whether records of this kind can carry a `tombstone_at`. */
  tombstoned: boolean
}

/**
 * Entity kinds. The `kind:id` vocabulary is shared with `opTarget` (ops.ts), so
 * the recorder's dirty keys, these chunk keys and the tombstone pin's keys are
 * all the same strings; `_everyOpKindIsSharded` below proves the two
 * declarations agree.
 *
 * The tombstone pin ignores `ws` (workspace records carry no tombstone, so they
 * are never pruned), but it is a map field of UserCrdtState and therefore must
 * be sharded here.
 */
const SHARDS = {
  // Each schema is widened to the non-generic `DescMessage` on purpose: the
  // codecs below are driven by a runtime lookup, so a per-kind message TYPE
  // would only push a cast to every call site.
  //
  // `tombstoned` says whether records of this kind can carry a `tombstone_at`,
  // and therefore whether the client-side GC has anything to collect from it.
  // Declared HERE rather than as a list beside the GC so a new kind states its
  // own answer once, next to its field and schema.
  node: { field: 'nodes', schema: NodeRecordSchema as DescMessage, tombstoned: true },
  tab: { field: 'tabs', schema: TabRecordSchema as DescMessage, tombstoned: true },
  fw: { field: 'floatingWindows', schema: FloatingWindowRecordSchema as DescMessage, tombstoned: true },
  ws: { field: 'workspaces', schema: WorkspaceContentsRecordSchema as DescMessage, tombstoned: false },
} as const satisfies Record<string, ShardSpec>

export type ChunkKind = keyof typeof SHARDS

/** Every kind, for whole-state walks. */
export const CHUNK_KINDS = Object.keys(SHARDS) as ChunkKind[]

/**
 * The kinds whose records can carry a tombstone — the only ones the client-side
 * tombstone GC has to walk. Derived from SHARDS so it cannot fall out of step
 * with it.
 */
export const TOMBSTONED_CHUNK_KINDS = CHUNK_KINDS.filter(kind => SHARDS[kind].tombstoned)

/**
 * The entity map `kind` shards, as a plain record.
 *
 * One home for the `state[SHARDS[kind].field]` lookup and its cast, which five
 * call sites in this module were each spelling out — and which the tombstone GC
 * in pendingOps.ts was open-coding as its own kind->map table entirely.
 */
export function entityMapFor(state: UserCrdtState, kind: ChunkKind): Record<string, unknown> {
  return state[SHARDS[kind].field] as Record<string, unknown>
}

/**
 * Compile-time proof that every map field of `UserCrdtState` is claimed by a
 * kind above. Add a map to the proto without adding a shard for it and this
 * fails to typecheck -- which is the point: an unsharded map would silently
 * ride along in the HEADER and put the whole-account serialize this module
 * exists to remove straight back on the checkpoint hot path.
 */
type ClaimedField = (typeof SHARDS)[ChunkKind]['field']
const _everyMapIsSharded: ShardedField extends ClaimedField ? true : never = true
void _everyMapIsSharded

/** True when `kind` names a shard this build knows. */
export function isChunkKind(kind: string): kind is ChunkKind {
  return Object.hasOwn(SHARDS, kind)
}

/** One entity's identity within an owner's checkpoint. */
export interface EntityRef {
  kind: ChunkKind
  entityId: string
}

/** One entity's persisted chunk. */
export interface EntityChunk extends EntityRef {
  /** `toBinary(<kind's record schema>, record)`. */
  bytes: Uint8Array
}

/** The `kind:id` key the dirty set and the tombstone diff both speak. */
export function entityKey(kind: ChunkKind, entityId: string): string {
  return `${kind}:${entityId}`
}

/**
 * Split a `kind:id` key. Returns undefined for a key naming an unknown kind.
 *
 * Splits at the FIRST colon only: entity ids may contain colons, kinds may not.
 */
export function parseEntityKey(key: string): EntityRef | undefined {
  const sep = key.indexOf(':')
  if (sep < 0)
    return undefined
  const kind = key.slice(0, sep)
  return isChunkKind(kind) ? { kind, entityId: key.slice(sep + 1) } : undefined
}

/**
 * Serialize the checkpoint HEADER: the state with every sharded map emptied.
 *
 * A shallow spread of the message object, so a scalar field added to
 * `UserCrdtState` rides along with no change here, and only the maps named by
 * `SHARDS` are blanked (the assertion above is what keeps that list complete).
 * The spread is not a deep clone, but nothing mutates the copy -- it exists
 * only to be handed to `toBinary` in this same synchronous call.
 */
export function serializeHeader(state: UserCrdtState): Uint8Array {
  const header = { ...state } as UserCrdtState
  for (const { field } of Object.values(SHARDS)) {
    // One narrow cast: `field` is a union of four map keys, so a direct
    // assignment would demand a value assignable to all four at once.
    ;(header as unknown as Record<string, unknown>)[field] = {}
  }
  return toBinary(UserCrdtStateSchema, header)
}

/**
 * Serialize ONE entity, or undefined when it is absent from `state` (removed by
 * an op or by the tombstone prune — the caller turns that into a chunk delete).
 */
export function serializeEntity(state: UserCrdtState, ref: EntityRef): Uint8Array | undefined {
  const spec = SHARDS[ref.kind]
  const record = (state[spec.field] as Record<string, Message>)[ref.entityId]
  return record === undefined ? undefined : toBinary(spec.schema, record)
}

/** Every entity in `state`, as chunks — the payload of a FULL rewrite. */
function serializeAllEntities(state: UserCrdtState): EntityChunk[] {
  const chunks: EntityChunk[] = []
  for (const kind of CHUNK_KINDS) {
    const spec = SHARDS[kind]
    for (const [entityId, record] of Object.entries(state[spec.field] as Record<string, Message>))
      chunks.push({ kind, entityId, bytes: toBinary(spec.schema, record) })
  }
  return chunks
}

/**
 * The delta that rewrites an owner's checkpoint from scratch: the header plus
 * every entity, with `full` set so the store drops whatever chunks the owner
 * already had.
 *
 * The bootstrap / lineage-reset payload, and the only shape a fresh owner can
 * be given -- an incremental delta is meaningless without a base.
 */
export function fullCheckpointDelta(state: UserCrdtState): CheckpointDelta {
  return { headerBytes: serializeHeader(state), upserts: serializeAllEntities(state), deletes: [], full: true }
}

/** Parse a header blob back into a `UserCrdtState` with empty entity maps. */
export function parseHeader(bytes: Uint8Array): UserCrdtState {
  const state = fromBinary(UserCrdtStateSchema, bytes)
  // A header written by `serializeHeader` encodes no map entries, so the
  // decoded maps are already empty; assigning fresh objects only guards against
  // a blob written before this module existed (or by a hand-edited row).
  state.nodes = {}
  state.tabs = {}
  state.floatingWindows = {}
  state.workspaces = {}
  return state
}

/**
 * Merge one chunk into `state`, keyed by the ROW's entity id.
 *
 * The row key is authoritative, exactly as the map key was in the monolithic
 * blob: a proto map entry's key is serialized independently of any id field
 * inside its value, so keying off the row reproduces the old behaviour rather
 * than adding a new way for a valid state to be rejected.
 *
 * Throws on an undecodable blob; the caller's corruption policy decides what
 * that means (hydrate wipes and cold-starts).
 */
export function applyChunk(state: UserCrdtState, kind: ChunkKind, entityId: string, bytes: Uint8Array): void {
  const spec = SHARDS[kind]
  ;(state[spec.field] as Record<string, Message>)[entityId] = fromBinary(spec.schema, bytes)
}

/**
 * Every entity key currently present in `state`, grouped by kind.
 *
 * Taken before `compactTombstones()` so the recorder can diff it against the
 * post-prune maps: the prune drops tombstone shells whose tombstoning op may
 * have been recorded MANY checkpoints ago, so the dirty set alone cannot see
 * them and their chunks would be resurrected by the next cold start.
 *
 * O(entities) in `Object.keys` calls only — no per-entity allocation beyond the
 * key arrays, which reuse the map's existing string references. That is roughly
 * two orders of magnitude below the whole-account `toBinary` this module
 * replaces, and the prune it brackets is already an O(entities) walk.
 */
export function snapshotEntityKeys(state: UserCrdtState): Record<ChunkKind, string[]> {
  const snapshot = {} as Record<ChunkKind, string[]>
  for (const kind of CHUNK_KINDS)
    snapshot[kind] = Object.keys(state[SHARDS[kind].field])
  return snapshot
}

/** Keys in `snapshot` that are no longer present in `state`. */
export function keysRemovedSince(snapshot: Record<ChunkKind, string[]>, state: UserCrdtState): EntityRef[] {
  const removed: EntityRef[] = []
  for (const kind of CHUNK_KINDS) {
    const map = state[SHARDS[kind].field] as Record<string, unknown>
    for (const entityId of snapshot[kind]) {
      if (!Object.hasOwn(map, entityId))
        removed.push({ kind, entityId })
    }
  }
  return removed
}

/**
 * The entity keys one confirmed frame touches, or `null` when the frame's shape
 * is not understood.
 *
 * `null` means "assume everything" — the caller must fall back to a FULL
 * rewrite. That is the safe direction and the reason this returns a nullable
 * rather than an empty set: an entity whose chunk is stale on disk but absent
 * from the dirty set is never rewritten, so the next cold start resurrects the
 * pre-change record silently. Under-reporting corrupts; over-reporting only
 * costs one extra serialize.
 */
export function framedEntityKeys(frame: WatchUserEvent): Set<string> | null {
  const event = frame.event
  switch (event.case) {
    case 'batch': {
      const keys = new Set<string>()
      for (const op of event.value.ops) {
        const opKeys = opEntityKeys(op)
        if (opKeys === null)
          return null
        for (const key of opKeys)
          keys.add(key)
      }
      return keys
    }
    case 'entityMaterialized':
    case 'entityRemoved': {
      const keys = entityRefKeys(event.value.entity)
      return keys === null ? null : new Set(keys)
    }
    case 'batchEnd':
      // Advances the resume watermark only; the watermark rides in the header,
      // which every rewrite re-serializes unconditionally.
      return new Set()
    default:
      // `initial` / `delta` / `presence` / workspace-lifecycle notices, an empty
      // oneof, or an arm added after this switch was written. `applyFrames`
      // installs nothing for any of them today, but a NEW arm that does would
      // be invisible here -- so refuse to guess.
      return null
  }
}

/**
 * The entity keys a single op targets — one key, or none for an op that
 * installs nothing.
 *
 * A thin adapter over `opTarget` (ops.ts), which owns the mapping and is shared
 * with the tombstone pin so the two cannot disagree about a newly added op
 * kind. `unknown` becomes null here, which forces the caller's full-rewrite
 * fallback; the pin degrades in its own direction (prune nothing).
 */
function opEntityKeys(op: CrdtOp): readonly string[] | null {
  const target = opTarget(op)
  switch (target.case) {
    case 'entity':
      return [entityKey(target.kind, target.id)]
    case 'none':
      return []
    default:
      return null
  }
}

/**
 * Compile-time proof that every kind `opTarget` can name has a shard here. The
 * two vocabularies are declared independently -- `OpEntityKind` beside the op
 * builders, `ChunkKind` from the SHARDS table above -- so this is what keeps
 * them one vocabulary rather than two that happen to match today. Adding a kind
 * to either without the other fails to typecheck, which is the whole point of
 * routing `opEntityKeys` through `opTarget`.
 */
const _everyOpKindIsSharded: OpEntityKind extends ChunkKind ? true : never = true
void _everyOpKindIsSharded

/**
 * The entity keys an `EntityMaterialized` / `EntityRemoved` frame names. The two
 * oneofs differ in shape -- materialized carries the whole record, removed
 * carries just an identifier -- but name the same three kinds.
 */
function entityRefKeys(entity: EntityMaterialized['entity'] | EntityRemoved['entity']): readonly string[] | null {
  switch (entity.case) {
    case 'tab':
      return [entityKey('tab', entity.value.tabId)]
    case 'node':
      return [entityKey('node', entity.value.nodeId)]
    case 'floatingWindow':
      return [entityKey('fw', entity.value.windowId)]
    case 'nodeId':
      return [entityKey('node', entity.value)]
    case 'windowId':
      return [entityKey('fw', entity.value)]
    case undefined:
      // Empty oneof: `applyMaterializedCore` / `applyRemovedCore` install and
      // evict nothing, so nothing is dirtied.
      return []
    default: {
      // Unreachable today, and PROVEN so -- the same treatment `opEntityKeys`
      // gets from `_everyOpKindIsSharded`. Without it, adding a kind to either
      // oneof lands here silently, and `null` is not a free fallback: it makes
      // `framedEntityKeys` report "assume everything changed", which forces a
      // full O(account) re-serialize on every such frame -- precisely the cost
      // this module exists to remove. A build error is the cheaper failure.
      const exhaustive: never = entity
      void exhaustive
      return null
    }
  }
}
