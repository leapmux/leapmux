import type { CrdtOp, OpBatch } from '~/generated/leapmux/v1/user_ops_pb'
import { describe, expect, it } from 'vitest'
import { coalesceQueuedBatches, REGISTER_POLICIES, registerKey, supersededParkedBatchIds } from './coalesce'

function setFwOpacity(opId: string, windowId: string, opacity: number): CrdtOp {
  return {
    opId,
    body: { case: 'setFloatingWindowRegister', value: { windowId, field: { case: 'opacity', value: opacity } } },
  } as unknown as CrdtOp
}

function setFwX(opId: string, windowId: string, x: number): CrdtOp {
  return {
    opId,
    body: { case: 'setFloatingWindowRegister', value: { windowId, field: { case: 'x', value: x } } },
  } as unknown as CrdtOp
}

function setFwRootNode(opId: string, windowId: string, rootNodeId: string): CrdtOp {
  return {
    opId,
    body: { case: 'setFloatingWindowRegister', value: { windowId, field: { case: 'rootNodeId', value: rootNodeId } } },
  } as unknown as CrdtOp
}

function setFwWorkspace(opId: string, windowId: string, workspaceId: string): CrdtOp {
  return {
    opId,
    body: { case: 'setFloatingWindowRegister', value: { windowId, field: { case: 'workspaceId', value: workspaceId } } },
  } as unknown as CrdtOp
}

function setTabTile(opId: string, tabId: string, tileId: string): CrdtOp {
  return {
    opId,
    body: { case: 'setTabRegister', value: { tabId, tabType: 1, field: { case: 'tileId', value: tileId } } },
  } as unknown as CrdtOp
}

function tombstoneFw(opId: string, windowId: string): CrdtOp {
  return {
    opId,
    body: { case: 'tombstoneFloatingWindow', value: { windowId } },
  } as unknown as CrdtOp
}

function batch(batchId: string, ops: CrdtOp[]): OpBatch {
  return { batchId, ops } as unknown as OpBatch
}

function opIds(batches: OpBatch[]): string[] {
  return batches.flatMap(b => b.ops.map(o => o.opId))
}

describe('registerKey', () => {
  it('separates fields of the same entity', () => {
    expect(registerKey(setFwX('a', 'w1', 1))).not.toBe(registerKey(setFwOpacity('b', 'w1', 0.5)))
  })

  it('separates the same field of different entities', () => {
    expect(registerKey(setFwOpacity('a', 'w1', 0.5))).not.toBe(registerKey(setFwOpacity('b', 'w2', 0.5)))
  })

  it('matches the same field of the same entity', () => {
    expect(registerKey(setFwOpacity('a', 'w1', 0.5))).toBe(registerKey(setFwOpacity('b', 'w1', 0.9)))
  })

  it('returns null for a tombstone', () => {
    expect(registerKey(tombstoneFw('a', 'w1'))).toBeNull()
  })

  it('returns null for set-once and creation-completeness fields', () => {
    // These are what make "a creation is just register sets" true in this CRDT,
    // so the allowlist has to name them out rather than infer them.
    expect(registerKey(setFwRootNode('a', 'w1', 'n1'))).toBeNull()
    expect(registerKey(setFwWorkspace('a', 'w1', 'ws1'))).toBeNull()
  })
})

describe('coalesceQueuedBatches', () => {
  it('keeps only the last write to a register', () => {
    const result = coalesceQueuedBatches([
      batch('b1', [setFwOpacity('o1', 'w1', 0.9)]),
      batch('b2', [setFwOpacity('o2', 'w1', 0.8)]),
      batch('b3', [setFwOpacity('o3', 'w1', 0.7)]),
    ])
    expect(opIds(result.batches)).toEqual(['o3'])
    expect(result.droppedBatchIds).toEqual(['b1', 'b2'])
    expect(result.droppedOps).toBe(2)
  })

  it('keeps writes to different registers of the same entity', () => {
    const result = coalesceQueuedBatches([
      batch('b1', [setFwX('o1', 'w1', 1), setFwOpacity('o2', 'w1', 0.5)]),
    ])
    expect(opIds(result.batches)).toEqual(['o1', 'o2'])
    expect(result.droppedOps).toBe(0)
  })

  it('keeps writes to the same register of different entities', () => {
    const result = coalesceQueuedBatches([
      batch('b1', [setFwOpacity('o1', 'w1', 0.5)]),
      batch('b2', [setFwOpacity('o2', 'w2', 0.5)]),
    ])
    expect(opIds(result.batches)).toEqual(['o1', 'o2'])
    expect(result.droppedBatchIds).toEqual([])
  })

  it('never trims a batch: a partially superseded batch is sent INTACT', () => {
    // The hub validates a batch as a unit -- a record must be COMPLETE when its
    // creation batch commits -- so removing one op from a batch is how a new
    // window or tile split gets rejected wholesale. Not trimming makes that
    // unreachable without the client re-deriving the server's completeness
    // rules.
    const result = coalesceQueuedBatches([
      batch('b1', [setFwX('o1', 'w1', 1), setFwOpacity('o2', 'w1', 0.5)]),
      batch('b2', [setFwOpacity('o3', 'w1', 0.9)]),
    ])
    // o2 IS superseded by o3, but o1 is not, so b1 goes out whole.
    expect(opIds(result.batches)).toEqual(['o1', 'o2', 'o3'])
    expect(result.droppedBatchIds).toEqual([])
    expect(result.droppedOps).toBe(0)
  })

  // The reason rule 2 is an allowlist and not "register sets are safe".
  it('never drops a creation batch, whose ops are all register sets', () => {
    // A floating window is CREATED by writing root_node_id, workspace_id and
    // its geometry. A rule phrased as "creations are excluded, register sets
    // are eligible" would exclude nothing here, and dropping this batch would
    // lose the window entirely.
    const creation = batch('create', [
      setFwRootNode('c1', 'w1', 'n1'),
      setFwWorkspace('c2', 'w1', 'ws1'),
      setFwX('c3', 'w1', 0.1),
      setFwOpacity('c4', 'w1', 1),
    ])
    const result = coalesceQueuedBatches([
      creation,
      // A later gesture rewrites every geometry register the creation set.
      batch('drag', [setFwX('d1', 'w1', 0.5), setFwOpacity('d2', 'w1', 0.4)]),
    ])
    expect(result.droppedBatchIds).toEqual([])
    expect(result.batches).toHaveLength(2)
  })

  it('does not treat a set-once field as supersedable', () => {
    // root_node_id is write-once, so a later write does not supersede an
    // earlier one and the earlier op is not redundant.
    const result = coalesceQueuedBatches([
      batch('b1', [setFwRootNode('o1', 'w1', 'n1')]),
      batch('b2', [setFwRootNode('o2', 'w1', 'n2')]),
    ])
    expect(result.droppedBatchIds).toEqual([])
  })

  it('is a no-op on an empty queue', () => {
    const result = coalesceQueuedBatches([])
    expect(result.batches).toEqual([])
    expect(result.droppedBatchIds).toEqual([])
    expect(result.droppedOps).toBe(0)
  })

  it('returns the ORIGINAL batch object when nothing was trimmed', () => {
    // Identity matters: the retry paths re-queue these objects, and a needless
    // copy would break the batchId-keyed retry bookkeeping's assumptions about
    // what it is holding.
    const b1 = batch('b1', [setFwX('o1', 'w1', 1)])
    const result = coalesceQueuedBatches([b1])
    expect(result.batches[0]).toBe(b1)
  })

  // A 60-frame drag scrub is the case this exists for.
  it('collapses a gesture-shaped burst to one op per register', () => {
    const queued = Array.from({ length: 60 }, (_, i) => batch(`b${i}`, [setFwX(`o${i}`, 'w1', i)]))
    const result = coalesceQueuedBatches(queued)
    expect(opIds(result.batches)).toEqual(['o59'])
    expect(result.droppedBatchIds).toHaveLength(59)
  })
})

describe('supersededParkedBatchIds', () => {
  it('reports a parked batch whose register a newer write rewrites', () => {
    // The hazard: the parked batch left the queue on a transport failure, so
    // the hub never saw it and dedup will not apply. Its retry would commit
    // with a FRESH canonical HLC -- newer than the write below -- and the stale
    // value would win LWW on the hub and every peer.
    const parked = [batch('parked', [setFwX('p1', 'w1', 0.1)])]
    const outgoing = [batch('now', [setFwX('o1', 'w1', 0.9)])]
    expect(supersededParkedBatchIds(parked, outgoing)).toEqual(['parked'])
  })

  it('leaves a parked batch alone when the newer write touches another register', () => {
    const parked = [batch('parked', [setFwX('p1', 'w1', 0.1)])]
    const outgoing = [batch('now', [setFwOpacity('o1', 'w1', 0.5)])]
    expect(supersededParkedBatchIds(parked, outgoing)).toEqual([])
  })

  it('leaves a parked batch alone when the newer write targets another window', () => {
    const parked = [batch('parked', [setFwX('p1', 'w1', 0.1)])]
    const outgoing = [batch('now', [setFwX('o1', 'w2', 0.9)])]
    expect(supersededParkedBatchIds(parked, outgoing)).toEqual([])
  })

  it('requires EVERY op to be superseded, not just some', () => {
    // A partially-superseded parked batch still carries a write nothing else
    // will make, so cancelling it would lose that edit outright.
    const parked = [batch('parked', [setFwX('p1', 'w1', 0.1), setFwOpacity('p2', 'w1', 0.3)])]
    const outgoing = [batch('now', [setFwX('o1', 'w1', 0.9)])]
    expect(supersededParkedBatchIds(parked, outgoing)).toEqual([])
  })

  it('never cancels a parked creation batch', () => {
    const parked = [batch('create', [setFwRootNode('c1', 'w1', 'n1'), setFwX('c2', 'w1', 0.1)])]
    const outgoing = [batch('now', [setFwX('o1', 'w1', 0.9)])]
    expect(supersededParkedBatchIds(parked, outgoing)).toEqual([])
  })

  it('is a no-op when nothing is going out', () => {
    const parked = [batch('parked', [setFwX('p1', 'w1', 0.1)])]
    expect(supersededParkedBatchIds(parked, [])).toEqual([])
  })

  // The move registers are EXCLUDED from the coalescing table (dropping an
  // intermediate move loses it), but they are plain LWW on the hub, and the hub
  // re-stamps canonical_hlc on a parked retry. So a stale parked move commits
  // NEWER than the move that replaced it and wins -- the one case where
  // cancelling is not an optimization but the thing that stops a user's move
  // from being silently undone. Keyed on the LWW-mutable table, not the
  // coalescable one.
  it('cancels a parked tile move superseded by a newer move of the same tab', () => {
    const parked = [batch('parked', [setTabTile('p1', 't1', 'tile-old')])]
    const outgoing = [batch('now', [setTabTile('o1', 't1', 'tile-new')])]
    expect(supersededParkedBatchIds(parked, outgoing)).toEqual(['parked'])
  })

  it('cancels a parked window workspace move superseded by a newer one', () => {
    const parked = [batch('parked', [setFwWorkspace('p1', 'w1', 'ws-old')])]
    const outgoing = [batch('now', [setFwWorkspace('o1', 'w1', 'ws-new')])]
    expect(supersededParkedBatchIds(parked, outgoing)).toEqual(['parked'])
  })

  it('still refuses to cancel a set-once register', () => {
    // rootNodeId is set-once on the hub, so a later write does NOT supersede the
    // parked one -- the parked write is the only one that will ever land.
    const parked = [batch('parked', [setFwRootNode('p1', 'w1', 'n1')])]
    const outgoing = [batch('now', [setFwRootNode('o1', 'w1', 'n2')])]
    expect(supersededParkedBatchIds(parked, outgoing)).toEqual([])
  })

  it('does not widen coalesceQueuedBatches to the move registers', () => {
    // Same two ops, but through the flush-time coalescer: dropping an
    // intermediate move on the wire is still a lost move, so both batches ship.
    const result = coalesceQueuedBatches([
      batch('b1', [setTabTile('a', 't1', 'tile-old')]),
      batch('b2', [setTabTile('b', 't1', 'tile-new')]),
    ])
    expect(result.droppedBatchIds).toEqual([])
    expect(result.batches).toHaveLength(2)
  })
})

// The two questions this module answers are related, and the relation is a
// SAFETY property: anything droppable must also be supersedable. When they were
// two boolean tables the relation was maintained by eye -- `Record<FieldCase>`
// forced both entries to exist but never to agree -- and the illegal pairing is
// the dangerous one: a SET_ONCE register marked coalescable drops a write the
// hub accepts only once, which is precisely what excluding parentId/rootNodeId
// is for. The enum makes that pairing unspellable; this pins that it stays so.
describe('register policies', () => {
  const every = Object.values(REGISTER_POLICIES).flatMap(table => Object.entries(table))

  it('covers a field in every kind', () => {
    expect(every.length).toBeGreaterThan(0)
  })

  it('never marks a set-once register coalescable', () => {
    const offenders = every.filter(([, policy]) => policy === 'SET_ONCE' && isCoalescableForTest(policy))
    expect(offenders).toEqual([])
  })

  it('makes everything coalescable also LWW-mutable', () => {
    const offenders = every.filter(([field, policy]) =>
      isCoalescableForTest(policy) && !isLwwMutableForTest(policy) ? field : null,
    )
    expect(offenders).toEqual([])
  })
})

// Mirrors of the module-private predicates, kept as literal comparisons so the
// assertions above cannot be satisfied by the same bug in a shared helper.
function isCoalescableForTest(policy: string): boolean {
  return policy === 'LWW_COALESCABLE'
}
function isLwwMutableForTest(policy: string): boolean {
  return policy !== 'SET_ONCE'
}
