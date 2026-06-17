import type { OrgOp } from '~/generated/leapmux/v1/org_ops_pb'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { NodeKind, NodeRecordSchema, OrgCrdtStateSchema, TabRecordSchema } from '~/generated/leapmux/v1/org_crdt_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { buildChildIndex, HLCClock } from '~/lib/crdt'
import { buildCloseSubtreeOps, buildCloseTileOps } from '~/stores/tileOps'

// tileOps is the shared op-builder primitive used by both layoutOps
// (main tree) and floatingWindowOps (inner tree). These tests pin the
// shape: leaf-only close, undo-split rewiring, descendant walk, and
// migrate-vs-tombstone semantics.

function mkCtx() {
  return {
    orgId: 'org',
    originClientId: 'client',
    clock: new HLCClock('client'),
  }
}

function mkHlc(p: bigint) {
  return { $typeName: 'leapmux.v1.HLC' as const, physical: p, logical: 0n, clientId: 'seed' }
}

function mkLeafNode(id: string, parentId: string = '') {
  return create(NodeRecordSchema, {
    nodeId: id,
    parentId,
    kind: { value: NodeKind.LEAF, hlc: mkHlc(1n) },
  })
}

function mkSplitNode(id: string, parentId: string = '') {
  return create(NodeRecordSchema, {
    nodeId: id,
    parentId,
    kind: { value: NodeKind.SPLIT, hlc: mkHlc(1n) },
  })
}

function mkGridNode(id: string, parentId: string = '') {
  return create(NodeRecordSchema, {
    nodeId: id,
    parentId,
    kind: { value: NodeKind.GRID, hlc: mkHlc(1n) },
  })
}

function mkTab(tabId: string, tileId: string) {
  return create(TabRecordSchema, {
    tabType: TabType.AGENT,
    tabId,
    tileId: { value: tileId, hlc: mkHlc(2n) },
  })
}

describe('buildCloseTileOps', () => {
  it('tombstones the tile and its tabs when there is no SPLIT parent', () => {
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: { root: mkLeafNode('root'), tile: mkLeafNode('tile', 'root') },
      tabs: { 'tab-1': mkTab('tab-1', 'tile') },
    })
    const ops = buildCloseTileOps(mkCtx(), state, 'tile')
    const kinds = ops.map(o => o.body.case)
    expect(kinds).toEqual(['tombstoneTab', 'tombstoneNode'])
  })

  it('undo-splits a 2-child SPLIT parent: migrates sibling tabs and collapses parent', () => {
    // Tree: parent (SPLIT) → [closing (LEAF), sibling (LEAF)]
    // Tabs:  tab-A on closing, tab-B on sibling
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: {
        root: mkLeafNode('root'),
        parent: mkSplitNode('parent', 'root'),
        closing: mkLeafNode('closing', 'parent'),
        sibling: mkLeafNode('sibling', 'parent'),
      },
      tabs: {
        'tab-A': mkTab('tab-A', 'closing'),
        'tab-B': mkTab('tab-B', 'sibling'),
      },
    })
    const ops = buildCloseTileOps(mkCtx(), state, 'closing')
    const kinds = ops.map(o => o.body.case)
    // Expected sequence:
    //   tombstoneTab(tab-A), tombstoneNode(closing),
    //   setTabRegister(tab-B, tile=parent), setTabRegister(tab-B, position),
    //   tombstoneNode(sibling), setNodeRegister(parent, kind=LEAF)
    expect(kinds).toContain('tombstoneTab')
    expect(kinds).toContain('tombstoneNode')
    expect(kinds).toContain('setTabRegister')
    expect(kinds).toContain('setNodeRegister')
    // The parent must be flipped back to LEAF.
    const lastNodeReg = ops.find(o => o.body.case === 'setNodeRegister'
      && o.body.value.nodeId === 'parent'
      && o.body.value.field.case === 'kind')
    expect(lastNodeReg).toBeDefined()
    // The sibling must be tombstoned in the same batch.
    const sibTombstone = ops.find(o => o.body.case === 'tombstoneNode' && o.body.value.nodeId === 'sibling')
    expect(sibTombstone).toBeDefined()
  })

  it('does NOT undo-split when parent has more than 2 live children', () => {
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: {
        root: mkLeafNode('root'),
        parent: mkSplitNode('parent', 'root'),
        a: mkLeafNode('a', 'parent'),
        b: mkLeafNode('b', 'parent'),
        c: mkLeafNode('c', 'parent'),
      },
    })
    const ops = buildCloseTileOps(mkCtx(), state, 'a')
    // No sibling migration, no parent collapse — just close the tile.
    expect(ops.filter(o => o.body.case === 'setNodeRegister')).toHaveLength(0)
    expect(ops.filter(o => o.body.case === 'tombstoneNode')).toHaveLength(1)
  })

  it('does NOT undo-split when parent is a GRID (only SPLIT triggers the rewrite)', () => {
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: {
        root: mkLeafNode('root'),
        parent: create(NodeRecordSchema, {
          nodeId: 'parent',
          parentId: 'root',
          kind: { value: NodeKind.GRID, hlc: mkHlc(1n) },
        }),
        closing: mkLeafNode('closing', 'parent'),
        sibling: mkLeafNode('sibling', 'parent'),
      },
    })
    const ops = buildCloseTileOps(mkCtx(), state, 'closing')
    // No sibling tombstone.
    expect(ops.find(o => o.body.case === 'tombstoneNode' && o.body.value.nodeId === 'sibling')).toBeUndefined()
  })

  // Regression: closing a sibling-of-grid leaf used to tombstone the
  // GRID sibling and flip the parent SPLIT to LEAF, orphaning every
  // cell + every tab whose tile_id is one of the cells. The validator
  // then rejected the batch with
  // BATCH_REJECTION_TAB_PLACEMENT_INVALID.
  //
  // The fix: when the sibling isn't a LEAF, skip the inverse-split
  // entirely. The projection's single-child SPLIT collapse already
  // re-keys the GRID to the parent's slot at render time, so no
  // rewiring is needed.
  it('does NOT undo-split when the surviving sibling is a GRID with its own cells/tabs', () => {
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: {
        root: mkLeafNode('root'),
        parent: mkSplitNode('parent', 'root'),
        grid: mkGridNode('grid', 'parent'),
        cellA: mkLeafNode('cellA', 'grid'),
        cellB: mkLeafNode('cellB', 'grid'),
        emptyLeaf: mkLeafNode('emptyLeaf', 'parent'),
      },
      tabs: {
        'tab-A': mkTab('tab-A', 'cellA'),
        'tab-B': mkTab('tab-B', 'cellB'),
      },
    })
    const ops = buildCloseTileOps(mkCtx(), state, 'emptyLeaf')
    // Only the closing leaf is tombstoned. The GRID + cells stay alive.
    const tombstonedNodes = ops
      .filter(o => o.body.case === 'tombstoneNode')
      .map(o => o.body.case === 'tombstoneNode' ? o.body.value.nodeId : '')
    expect(tombstonedNodes).toEqual(['emptyLeaf'])
    // No tab migration; no kind flip.
    expect(ops.filter(o => o.body.case === 'setTabRegister')).toHaveLength(0)
    expect(ops.filter(o => o.body.case === 'setNodeRegister')).toHaveLength(0)
  })

  it('does NOT undo-split when the surviving sibling is a SPLIT', () => {
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: {
        root: mkLeafNode('root'),
        parent: mkSplitNode('parent', 'root'),
        innerSplit: mkSplitNode('innerSplit', 'parent'),
        leafA: mkLeafNode('leafA', 'innerSplit'),
        leafB: mkLeafNode('leafB', 'innerSplit'),
        emptyLeaf: mkLeafNode('emptyLeaf', 'parent'),
      },
      tabs: { 'tab-A': mkTab('tab-A', 'leafA') },
    })
    const ops = buildCloseTileOps(mkCtx(), state, 'emptyLeaf')
    const tombstonedNodes = ops
      .filter(o => o.body.case === 'tombstoneNode')
      .map(o => o.body.case === 'tombstoneNode' ? o.body.value.nodeId : '')
    expect(tombstonedNodes).toEqual(['emptyLeaf'])
    expect(ops.filter(o => o.body.case === 'setTabRegister')).toHaveLength(0)
    expect(ops.filter(o => o.body.case === 'setNodeRegister')).toHaveLength(0)
  })

  it('does NOT undo-split when the closing tile is a root (parentId == "")', () => {
    // A tile-with-no-parent case shouldn't trigger the SPLIT-parent
    // logic (there's no parent). This isn't a valid invocation —
    // callers must not close registered roots — but the builder
    // should still produce a sensible op sequence.
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: { tile: mkLeafNode('tile') },
    })
    const ops = buildCloseTileOps(mkCtx(), state, 'tile')
    expect(ops.map(o => o.body.case)).toEqual(['tombstoneNode'])
  })
})

describe('buildCloseSubtreeOps', () => {
  it('tombstones every descendant leaves-first plus the root by default', () => {
    // Tree: root (SPLIT) → [a (LEAF), b (SPLIT) → [c (LEAF)]]
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: {
        root: mkSplitNode('root'),
        a: mkLeafNode('a', 'root'),
        b: mkSplitNode('b', 'root'),
        c: mkLeafNode('c', 'b'),
      },
    })
    const ops = buildCloseSubtreeOps(mkCtx(), state, 'root')
    const tombstoned = ops
      .filter(o => o.body.case === 'tombstoneNode')
      .map(o => o.body.case === 'tombstoneNode' ? o.body.value.nodeId : '')
    expect(tombstoned).toContain('a')
    expect(tombstoned).toContain('b')
    expect(tombstoned).toContain('c')
    expect(tombstoned).toContain('root')
    // Leaves-first ordering: c before b, a or c before root.
    expect(tombstoned.indexOf('c')).toBeLessThan(tombstoned.indexOf('b'))
  })

  it('omits the root tombstone when tombstoneRoot=false', () => {
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: {
        root: mkSplitNode('root'),
        a: mkLeafNode('a', 'root'),
      },
    })
    const ops = buildCloseSubtreeOps(mkCtx(), state, 'root', { tombstoneRoot: false })
    const tombstoned = ops
      .filter(o => o.body.case === 'tombstoneNode')
      .map(o => o.body.case === 'tombstoneNode' ? o.body.value.nodeId : '')
    expect(tombstoned).toContain('a')
    expect(tombstoned).not.toContain('root')
  })

  it('migrates tabs to the target tile when migrateTabsTo is set (no tombstoneTab ops)', () => {
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: {
        root: mkSplitNode('root'),
        a: mkLeafNode('a', 'root'),
        b: mkLeafNode('b', 'root'),
        survivor: mkLeafNode('survivor'),
      },
      tabs: {
        'tab-A': mkTab('tab-A', 'a'),
        'tab-B': mkTab('tab-B', 'b'),
      },
    })
    const ops = buildCloseSubtreeOps(mkCtx(), state, 'root', {
      migrateTabsTo: 'survivor',
      tombstoneRoot: true,
    })
    // No tab tombstones.
    expect(ops.find(o => o.body.case === 'tombstoneTab')).toBeUndefined()
    // Every tab gets a tile_id set to 'survivor'.
    const migrationOps = ops.filter(o => o.body.case === 'setTabRegister' && o.body.value.field.case === 'tileId')
    expect(migrationOps).toHaveLength(2)
    for (const op of migrationOps) {
      if (op.body.case !== 'setTabRegister' || op.body.value.field.case !== 'tileId')
        throw new Error('expected a setTabRegister(tileId) op')
      expect(op.body.value.field.value).toBe('survivor')
    }
  })

  it('tombstones tabs when migrateTabsTo is unset', () => {
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: { root: mkLeafNode('root') },
      tabs: { 'tab-A': mkTab('tab-A', 'root') },
    })
    const ops = buildCloseSubtreeOps(mkCtx(), state, 'root')
    expect(ops.find(o => o.body.case === 'tombstoneTab')).toBeDefined()
    expect(ops.find(o => o.body.case === 'setTabRegister')).toBeUndefined()
  })

  it('is a degenerate no-op-ish on a single leaf when migrateTabsTo+tombstoneRoot=false', () => {
    // Edge case: a leaf with no tabs and tombstoneRoot=false yields
    // exactly zero ops. Used by callers that want only the subtree
    // tombstones without affecting the root.
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: { root: mkLeafNode('root') },
    })
    const ops = buildCloseSubtreeOps(mkCtx(), state, 'root', { tombstoneRoot: false })
    expect(ops).toHaveLength(0)
  })
})

// `buildCloseTileOps` and `buildCloseSubtreeOps` now accept an
// optional `childIndex` so callers rendering many subtrees from the
// same state can share a single O(N) `buildChildIndex` pass instead
// of paying for one rebuild per close call. Equivalence with the
// build-internally branch is the regression-prevention contract: an
// honest caller threading the index in must get the exact same op
// sequence as a caller that doesn't. The opId field is freshly minted
// per op so we compare the deterministic structural shape (body case
// + payload fields) rather than full object equality.
describe('precomputed childIndex equivalence', () => {
  function bodyShape(op: OrgOp) {
    return { case: op.body.case, value: op.body.value }
  }

  it('buildCloseTileOps: no SPLIT-parent path matches build-internally output', () => {
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: { root: mkLeafNode('root'), tile: mkLeafNode('tile', 'root') },
      tabs: { 'tab-1': mkTab('tab-1', 'tile') },
    })
    const idx = buildChildIndex(state)
    const a = buildCloseTileOps(mkCtx(), state, 'tile')
    const b = buildCloseTileOps(mkCtx(), state, 'tile', idx)
    expect(b.map(bodyShape)).toEqual(a.map(bodyShape))
  })

  it('buildCloseTileOps: undo-split path matches build-internally output', () => {
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: {
        root: mkLeafNode('root'),
        parent: mkSplitNode('parent', 'root'),
        closing: mkLeafNode('closing', 'parent'),
        sibling: mkLeafNode('sibling', 'parent'),
      },
      tabs: {
        'tab-A': mkTab('tab-A', 'closing'),
        'tab-B': mkTab('tab-B', 'sibling'),
      },
    })
    const idx = buildChildIndex(state)
    const a = buildCloseTileOps(mkCtx(), state, 'closing')
    const b = buildCloseTileOps(mkCtx(), state, 'closing', idx)
    expect(b.map(bodyShape)).toEqual(a.map(bodyShape))
  })

  it('buildCloseSubtreeOps: nested subtree matches build-internally output', () => {
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: {
        root: mkSplitNode('root'),
        a: mkLeafNode('a', 'root'),
        b: mkSplitNode('b', 'root'),
        c: mkLeafNode('c', 'b'),
      },
      tabs: {
        'tab-A': mkTab('tab-A', 'a'),
        'tab-C': mkTab('tab-C', 'c'),
      },
    })
    const idx = buildChildIndex(state)
    const a = buildCloseSubtreeOps(mkCtx(), state, 'root', {})
    const b = buildCloseSubtreeOps(mkCtx(), state, 'root', {}, idx)
    expect(b.map(bodyShape)).toEqual(a.map(bodyShape))
  })

  it('buildCloseSubtreeOps with migrateTabsTo + tombstoneRoot variations matches', () => {
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: {
        root: mkSplitNode('root'),
        a: mkLeafNode('a', 'root'),
        b: mkLeafNode('b', 'root'),
        survivor: mkLeafNode('survivor'),
      },
      tabs: {
        'tab-A': mkTab('tab-A', 'a'),
        'tab-B': mkTab('tab-B', 'b'),
      },
    })
    const idx = buildChildIndex(state)
    // Migrate variant.
    const aMig = buildCloseSubtreeOps(mkCtx(), state, 'root', { migrateTabsTo: 'survivor', tombstoneRoot: true })
    const bMig = buildCloseSubtreeOps(mkCtx(), state, 'root', { migrateTabsTo: 'survivor', tombstoneRoot: true }, idx)
    expect(bMig.map(bodyShape)).toEqual(aMig.map(bodyShape))
    // tombstoneRoot=false variant.
    const aNoRoot = buildCloseSubtreeOps(mkCtx(), state, 'root', { tombstoneRoot: false })
    const bNoRoot = buildCloseSubtreeOps(mkCtx(), state, 'root', { tombstoneRoot: false }, idx)
    expect(bNoRoot.map(bodyShape)).toEqual(aNoRoot.map(bodyShape))
  })

  it('buildCloseTileOps with parent that has > 2 live children matches', () => {
    const state = create(OrgCrdtStateSchema, {
      orgId: 'org',
      nodes: {
        root: mkLeafNode('root'),
        parent: mkSplitNode('parent', 'root'),
        x: mkLeafNode('x', 'parent'),
        y: mkLeafNode('y', 'parent'),
        z: mkLeafNode('z', 'parent'),
      },
    })
    const idx = buildChildIndex(state)
    const a = buildCloseTileOps(mkCtx(), state, 'x')
    const b = buildCloseTileOps(mkCtx(), state, 'x', idx)
    expect(b.map(bodyShape)).toEqual(a.map(bodyShape))
  })
})
