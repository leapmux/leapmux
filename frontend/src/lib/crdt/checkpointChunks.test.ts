import type { applyOp } from './apply'
import type { UserCrdtState } from '~/generated/leapmux/v1/user_crdt_pb'
import { create, toBinary } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import {
  NodeRecordSchema,
  UserCrdtStateSchema,
  WorkspaceContentsRecordSchema,
} from '~/generated/leapmux/v1/user_crdt_pb'
import { WatchUserEventSchema } from '~/generated/leapmux/v1/user_ops_pb'
import {
  applyChunk,
  CHUNK_KINDS,
  entityKey,
  framedEntityKeys,
  fullCheckpointDelta,
  isChunkKind,
  keysRemovedSince,
  parseEntityKey,
  parseHeader,
  serializeEntity,
  serializeHeader,
  snapshotEntityKeys,
} from './checkpointChunks'

const HLC = { physical: 10n, logical: 1n, clientId: 'c' }

function state(): UserCrdtState {
  return create(UserCrdtStateSchema, {
    userId: 'u',
    currentEpoch: 4n,
    maxHlc: HLC,
    compactionWatermark: { physical: 3n, logical: 0n, clientId: 'c' },
    opRetentionWatermark: { physical: 1n, logical: 0n, clientId: 'c' },
    nodes: { n1: { nodeId: 'n1', parentId: 'root', position: { value: 'a', hlc: HLC } } },
    tabs: { t1: { tabId: 't1', tabType: 1, position: { value: 'b', hlc: HLC } } },
    floatingWindows: { w1: { windowId: 'w1', rootNodeId: 'n1' } },
    workspaces: { ws1: { workspaceId: 'ws1', rootNodeId: 'n1' } },
  })
}

/** Round-trip a state through the header + chunk split, as hydrate would. */
function rebuild(source: UserCrdtState): UserCrdtState {
  const delta = fullCheckpointDelta(source)
  const rebuilt = parseHeader(delta.headerBytes)
  for (const chunk of delta.upserts) {
    // The store treats `kind` as an opaque string, so hydrate has to re-narrow
    // it -- exactly as loadHydrationState does before applying a chunk.
    if (!isChunkKind(chunk.kind))
      throw new Error(`unexpected chunk kind: ${chunk.kind}`)
    applyChunk(rebuilt, chunk.kind, chunk.entityId, chunk.bytes)
  }
  return rebuilt
}

function frame(init: Parameters<typeof create<typeof WatchUserEventSchema>>[1]) {
  return create(WatchUserEventSchema, init)
}

describe('serializeHeader', () => {
  it('drops every entity map but keeps every scalar', () => {
    const header = parseHeader(serializeHeader(state()))
    expect(header.nodes).toEqual({})
    expect(header.tabs).toEqual({})
    expect(header.floatingWindows).toEqual({})
    expect(header.workspaces).toEqual({})
    expect(header.userId).toBe('u')
    expect(header.currentEpoch).toBe(4n)
    expect(header.maxHlc).toEqual(create(UserCrdtStateSchema, { maxHlc: HLC }).maxHlc)
    expect(header.compactionWatermark?.physical).toBe(3n)
    expect(header.opRetentionWatermark?.physical).toBe(1n)
  })

  it('does not mutate the state it serializes', () => {
    // A shallow spread with blanked maps, not an edit of the live object -- the
    // recorder calls this on `mgr.state.confirmedState` itself.
    const s = state()
    serializeHeader(s)
    expect(Object.keys(s.nodes)).toEqual(['n1'])
    expect(Object.keys(s.workspaces)).toEqual(['ws1'])
  })

  it('is O(1) in account size: the header does not grow with the maps', () => {
    const small = state()
    const large = state()
    for (let i = 0; i < 500; i++)
      large.nodes[`n${i}`] = create(NodeRecordSchema, { nodeId: `n${i}` })
    expect(serializeHeader(large).byteLength).toBe(serializeHeader(small).byteLength)
    // ...while the monolithic blob it replaces very much does.
    expect(toBinary(UserCrdtStateSchema, large).byteLength)
      .toBeGreaterThan(toBinary(UserCrdtStateSchema, small).byteLength)
  })
})

describe('fullCheckpointDelta', () => {
  it('round-trips a state through header + chunks', () => {
    const source = state()
    expect(rebuild(source)).toEqual(source)
  })

  it('emits one chunk per entity, across every kind', () => {
    const delta = fullCheckpointDelta(state())
    expect(delta.full).toBe(true)
    expect(delta.deletes).toEqual([])
    expect(delta.upserts.map(c => `${c.kind}:${c.entityId}`).sort())
      .toEqual(['fw:w1', 'node:n1', 'tab:t1', 'ws:ws1'])
  })

  it('emits nothing but a header for an empty state', () => {
    const empty = create(UserCrdtStateSchema, { userId: 'u' })
    const delta = fullCheckpointDelta(empty)
    expect(delta.upserts).toEqual([])
    expect(rebuild(empty)).toEqual(empty)
  })

  it('round-trips an entity id containing a colon', () => {
    // Entity ids are opaque and the dirty-set key is `kind:id`, so the split
    // has to take the FIRST colon only.
    const source = state()
    source.workspaces['ws:with:colons'] = create(WorkspaceContentsRecordSchema, { workspaceId: 'ws:with:colons' })
    expect(rebuild(source)).toEqual(source)
    expect(parseEntityKey(entityKey('ws', 'ws:with:colons')))
      .toEqual({ kind: 'ws', entityId: 'ws:with:colons' })
  })
})

describe('applyChunk', () => {
  it('keys the record by the ROW id, matching how the map was keyed before', () => {
    const rebuilt = parseHeader(serializeHeader(state()))
    const bytes = serializeEntity(state(), { kind: 'node', entityId: 'n1' })!
    applyChunk(rebuilt, 'node', 'renamed', bytes)
    expect(Object.keys(rebuilt.nodes)).toEqual(['renamed'])
  })

  it('throws on an undecodable blob, so the caller can wipe and cold-start', () => {
    const rebuilt = parseHeader(serializeHeader(state()))
    expect(() => applyChunk(rebuilt, 'node', 'n1', new Uint8Array([0xFF, 0xFE, 0xFD]))).toThrow()
  })
})

describe('serializeEntity', () => {
  it('returns undefined for an entity that is not in the state', () => {
    // The signal the recorder turns into a chunk DELETE.
    expect(serializeEntity(state(), { kind: 'node', entityId: 'gone' })).toBeUndefined()
  })

  it('serializes each kind with its own record schema', () => {
    const s = state()
    for (const kind of CHUNK_KINDS)
      expect(serializeEntity(s, { kind, entityId: { node: 'n1', tab: 't1', fw: 'w1', ws: 'ws1' }[kind] })).toBeDefined()
  })
})

describe('isChunkKind', () => {
  it('accepts every declared kind and rejects anything else', () => {
    for (const kind of CHUNK_KINDS)
      expect(isChunkKind(kind)).toBe(true)
    expect(isChunkKind('nodes')).toBe(false)
    expect(isChunkKind('')).toBe(false)
    // Not fooled by Object.prototype members: a row claiming kind
    // "constructor" must be corruption, not a shard.
    expect(isChunkKind('constructor')).toBe(false)
    expect(isChunkKind('toString')).toBe(false)
  })
})

describe('parseEntityKey', () => {
  it('rejects a key with no separator or an unknown kind', () => {
    expect(parseEntityKey('node')).toBeUndefined()
    expect(parseEntityKey('bogus:x')).toBeUndefined()
  })

  it('accepts an empty entity id', () => {
    expect(parseEntityKey('ws:')).toEqual({ kind: 'ws', entityId: '' })
  })
})

describe('snapshotEntityKeys and keysRemovedSince', () => {
  it('reports nothing removed when the state is unchanged', () => {
    const s = state()
    expect(keysRemovedSince(snapshotEntityKeys(s), s)).toEqual([])
  })

  it('reports exactly the entities deleted since the snapshot', () => {
    // The recorder brackets `compactTombstones()` with this, because the prune
    // drops shells whose tombstoning op may have been recorded MANY checkpoints
    // ago -- so the dirty set alone cannot see them, and their chunks would be
    // resurrected by the next cold start.
    const s = state()
    const before = snapshotEntityKeys(s)
    delete s.nodes.n1
    delete s.workspaces.ws1
    expect(keysRemovedSince(before, s).map(r => entityKey(r.kind, r.entityId)).sort())
      .toEqual(['node:n1', 'ws:ws1'])
  })

  it('ignores entities ADDED since the snapshot', () => {
    const s = state()
    const before = snapshotEntityKeys(s)
    s.nodes.n2 = create(NodeRecordSchema, { nodeId: 'n2' })
    expect(keysRemovedSince(before, s)).toEqual([])
  })

  it('is unaffected by the snapshot being taken on the same live object', () => {
    // snapshotEntityKeys returns key ARRAYS, not a view: mutating the maps
    // afterwards must not retroactively change what was snapshotted.
    const s = state()
    const before = snapshotEntityKeys(s)
    delete s.tabs.t1
    expect(before.tab).toEqual(['t1'])
  })
})

describe('framedEntityKeys', () => {
  it('reports the entity a batch\'s ops touch, for every op arm', () => {
    const cases: Array<[Parameters<typeof applyOp>[1]['body'], string]> = [
      [{ case: 'setNodeRegister', value: { nodeId: 'n' } }, 'node:n'],
      [{ case: 'tombstoneNode', value: { nodeId: 'n' } }, 'node:n'],
      [{ case: 'setTabRegister', value: { tabId: 't' } }, 'tab:t'],
      [{ case: 'tombstoneTab', value: { tabId: 't' } }, 'tab:t'],
      [{ case: 'setFloatingWindowRegister', value: { windowId: 'w' } }, 'fw:w'],
      [{ case: 'tombstoneFloatingWindow', value: { windowId: 'w' } }, 'fw:w'],
      [{ case: 'setWorkspaceRootNode', value: { workspaceId: 'ws' } }, 'ws:ws'],
      [{ case: 'setWorkspaceRegister', value: { workspaceId: 'ws' } }, 'ws:ws'],
      [{ case: 'tombstoneWorkspace', value: { workspaceId: 'ws' } }, 'ws:ws'],
    ] as never
    for (const [body, expected] of cases) {
      const f = frame({ event: { case: 'batch', value: { batchId: 'b', ops: [{ body }] } } } as never)
      expect(framedEntityKeys(f), `${(body as { case: string }).case}`).toEqual(new Set([expected]))
    }
  })

  it('unions every op in one batch', () => {
    const f = frame({
      event: {
        case: 'batch',
        value: {
          batchId: 'b',
          ops: [
            { body: { case: 'setNodeRegister', value: { nodeId: 'n1' } } },
            { body: { case: 'setTabRegister', value: { tabId: 't1' } } },
            { body: { case: 'setNodeRegister', value: { nodeId: 'n1' } } },
          ],
        },
      },
    } as never)
    expect(framedEntityKeys(f)).toEqual(new Set(['node:n1', 'tab:t1']))
  })

  it('reports the entity an entityMaterialized frame installs', () => {
    expect(framedEntityKeys(frame({
      event: { case: 'entityMaterialized', value: { entity: { case: 'tab', value: { tabId: 't9' } } } },
    } as never))).toEqual(new Set(['tab:t9']))
    expect(framedEntityKeys(frame({
      event: { case: 'entityMaterialized', value: { entity: { case: 'node', value: { nodeId: 'n9' } } } },
    } as never))).toEqual(new Set(['node:n9']))
    expect(framedEntityKeys(frame({
      event: { case: 'entityMaterialized', value: { entity: { case: 'floatingWindow', value: { windowId: 'w9' } } } },
    } as never))).toEqual(new Set(['fw:w9']))
  })

  it('reports the entity an entityRemoved frame evicts', () => {
    expect(framedEntityKeys(frame({
      event: { case: 'entityRemoved', value: { entity: { case: 'tab', value: { tabId: 't9' } } } },
    } as never))).toEqual(new Set(['tab:t9']))
    expect(framedEntityKeys(frame({
      event: { case: 'entityRemoved', value: { entity: { case: 'nodeId', value: 'n9' } } },
    } as never))).toEqual(new Set(['node:n9']))
    expect(framedEntityKeys(frame({
      event: { case: 'entityRemoved', value: { entity: { case: 'windowId', value: 'w9' } } },
    } as never))).toEqual(new Set(['fw:w9']))
  })

  it('reports nothing for a batchEnd, which only moves the watermark', () => {
    // The watermark rides in the header, and every rewrite re-serializes that.
    expect(framedEntityKeys(frame({ event: { case: 'batchEnd', value: { atHlc: HLC } } } as never)))
      .toEqual(new Set())
  })

  it('reports nothing for an op or entity oneof that is EMPTY', () => {
    // applyOp / applyMaterializedCore fall through their switches and install
    // nothing, so nothing is dirtied.
    expect(framedEntityKeys(frame({ event: { case: 'batch', value: { batchId: 'b', ops: [{}] } } } as never)))
      .toEqual(new Set())
    expect(framedEntityKeys(frame({ event: { case: 'entityRemoved', value: {} } } as never)))
      .toEqual(new Set())
  })

  it('refuses to guess for a frame arm it does not understand', () => {
    // null forces the caller's FULL rewrite. Under-reporting is the one
    // unrecoverable direction: an entity whose chunk is stale on disk but
    // absent from the dirty set is never rewritten, so the next cold start
    // silently resurrects the pre-change record.
    expect(framedEntityKeys(frame({ event: { case: 'presence', value: {} } } as never))).toBeNull()
    expect(framedEntityKeys(frame({}))).toBeNull()
    expect(framedEntityKeys({ event: { case: 'somethingNew', value: {} } } as never)).toBeNull()
  })

  it('refuses to guess when ONE op of a batch is unrecognized', () => {
    // A future op arm this build has no case for. Patched in AFTER `create`,
    // which silently drops an unknown oneof case rather than storing it.
    const f = frame({
      event: {
        case: 'batch',
        value: {
          batchId: 'b',
          ops: [
            { body: { case: 'setNodeRegister', value: { nodeId: 'n1' } } },
            { body: { case: 'setTabRegister', value: { tabId: 't1' } } },
          ],
        },
      },
    } as never)
    const ops = (f.event as { value: { ops: Array<{ body: unknown }> } }).value.ops
    ops[1]!.body = { case: 'somethingNew', value: {} }
    expect(framedEntityKeys(f)).toBeNull()
  })
})
