import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { setCRDTBridge } from '~/lib/crdt'
import { createLayoutStore } from '~/stores/layout.store'
import { withTestBridge } from '../helpers/crdtBridge'

describe('createLayoutStore (projection-driven)', () => {
  it('renders the seeded root tile from the CRDT projection', () => {
    withTestBridge((_harness) => {
      const store = createLayoutStore()
      expect(store.state.root.type).toBe('leaf')
      expect(store.state.root.id).toBe('root-leaf')
      expect(store.getAllTileIds()).toEqual(['root-leaf'])
    }, { rootTileId: 'root-leaf' })
  })

  it('splitTile emits a 9-op batch that flips T LEAF→SPLIT in the projection', () => {
    withTestBridge((harness) => {
      const store = createLayoutStore()
      const childB = store.splitTile('root-leaf', 'horizontal')
      expect(childB).toBeTruthy()
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      // 9 register writes: 3 on T (kind/direction/ratios) + 3 on
      // childA + 3 on childB (kind/parentId/position each).
      expect(lastBatch?.ops.length).toBe(9)
      // Projection should show T as a SPLIT now.
      expect(store.state.root.type).toBe('split')
      expect(store.state.root.id).toBe('root-leaf')
      if (store.state.root.type === 'split') {
        expect(store.state.root.direction).toBe('horizontal')
        expect(store.state.root.children.length).toBe(2)
        expect(store.state.root.children[1].id).toBe(childB)
      }
    }, { rootTileId: 'root-leaf' })
  })

  it('makeGrid emits a batch with R*C cells under T', () => {
    withTestBridge((harness) => {
      const store = createLayoutStore()
      const result = store.makeGrid('root-leaf', 2, 3)
      expect(result.cellTileIds).toHaveLength(6)
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      // 5 grid registers + 3 ops per cell × 6 cells = 23.
      expect(lastBatch?.ops.length).toBe(23)
      expect(store.state.root.type).toBe('grid')
      if (store.state.root.type === 'grid') {
        expect(store.state.root.rows).toBe(2)
        expect(store.state.root.cols).toBe(3)
        expect(store.state.root.cells.length).toBe(6)
      }
    }, { rootTileId: 'root-leaf' })
  })

  it('splitTile then closeTile on a child sibling collapses the projection back to a single leaf', () => {
    withTestBridge((_harness) => {
      const store = createLayoutStore()
      const childB = store.splitTile('root-leaf', 'horizontal')!
      // closeTile on childB tombstones it; the projection's single-
      // child SPLIT collapse rule then renders the parent as childA.
      store.closeTile(childB)
      expect(store.state.root.type).toBe('leaf')
      // Single-child collapse re-keys the rendered leaf to T's
      // node_id (the parent SPLIT id), preserving identity.
      expect(store.state.root.id).toBe('root-leaf')
    }, { rootTileId: 'root-leaf' })
  })

  it('updateRatios emits a single op', () => {
    withTestBridge((harness) => {
      const store = createLayoutStore()
      store.splitTile('root-leaf', 'horizontal')
      const before = harness.pending.state.pendingBatches.length
      const ok = store.updateRatios('root-leaf', [0.3, 0.7])
      expect(ok).toBe(true)
      expect(harness.pending.state.pendingBatches.length - before).toBe(1)
      expect(harness.pending.state.pendingBatches.at(-1)?.ops.length).toBe(1)
    }, { rootTileId: 'root-leaf' })
  })

  it('without a wired bridge, mutators are no-ops', () => {
    createRoot((dispose) => {
      setCRDTBridge(null)
      const store = createLayoutStore()
      // splitTile under a null bridge should be a no-op (returns null).
      expect(store.splitTile('whatever', 'horizontal')).toBeNull()
      expect(store.makeGrid('whatever', 2, 2)).toEqual({ gridId: '', cellTileIds: [] })
      dispose()
    })
  })

  // Regression: pre-fix, closing one of two SPLIT children left the
  // SPLIT alive with one live cell. The projection's single-child
  // collapse rule re-keyed the survivor to the parent's id, but the
  // surviving tab's stored tile_id still pointed at the actual child
  // — so `tabsByTile(rendered tile id)` returned [] and the user saw
  // an empty tile though the sidebar still listed the tab.
  // emitCloseTile now detects "parent SPLIT will be left with one
  // live child" and emits an inverse-split: tombstone the closing
  // tile + sibling, migrate sibling tabs to the parent, flip the
  // parent's kind back to LEAF in place.
  it('closing one of two SPLIT children flips the parent back to LEAF', () => {
    withTestBridge((harness) => {
      const store = createLayoutStore()
      const childB = store.splitTile('root-leaf', 'horizontal')!

      store.closeTile(childB)

      const lastBatch = harness.pending.state.pendingBatches.at(-1)!
      // Ops include tombstoning the closing tile and the sibling, plus
      // SetNodeRegister(parent, kind=LEAF). No tombstone on the parent
      // (workspace root is protected).
      const opCases = lastBatch.ops.map((op) => {
        if (op.body.case === 'tombstoneNode')
          return `tombstoneNode:${op.body.value.nodeId}`
        if (op.body.case === 'setNodeRegister' && op.body.value.field?.case === 'kind')
          return `setNodeKind:${op.body.value.nodeId}`
        return op.body.case
      })
      expect(opCases.some(c => c.startsWith('tombstoneNode:'))).toBe(true)
      expect(opCases).toContain('setNodeKind:root-leaf')
      expect(opCases).not.toContain('tombstoneNode:root-leaf')
      // Projection collapses to the root LEAF.
      expect(store.state.root.type).toBe('leaf')
      expect(store.state.root.id).toBe('root-leaf')
    }, { rootTileId: 'root-leaf' })
  })

  // Regression: pre-fix, closing all tabs in a grid (the "finalize"
  // path that doesn't preserve tabs) called emitRemoveGrid, which
  // unconditionally tombstoned the grid node. When the grid IS the
  // workspace root, the hub rejected the batch with `root_node_
  // protected` and rolled back every op — agent tab was already
  // closed in a separate batch, but the grid cells stayed alive and
  // the user couldn't get rid of the empty grid.
  it('removeGrid on the workspace-root grid flips its kind to LEAF instead of tombstoning', () => {
    withTestBridge((harness) => {
      const store = createLayoutStore()
      store.makeGrid('root-leaf', 1, 2)

      store.removeGrid('root-leaf')

      const lastBatch = harness.pending.state.pendingBatches.at(-1)!
      const opCases = lastBatch.ops.map((op) => {
        if (op.body.case === 'tombstoneNode')
          return `tombstoneNode:${op.body.value.nodeId}`
        if (op.body.case === 'setNodeRegister' && op.body.value.field?.case === 'kind')
          return `setNodeKind:${op.body.value.nodeId}`
        return op.body.case
      })
      // The cells get tombstoned but the workspace root does not — its
      // kind register flips to LEAF instead.
      expect(opCases).not.toContain('tombstoneNode:root-leaf')
      expect(opCases).toContain('setNodeKind:root-leaf')
      // After the batch settles the projected root is a bare LEAF.
      expect(store.state.root.type).toBe('leaf')
      expect(store.state.root.id).toBe('root-leaf')
    }, { rootTileId: 'root-leaf' })
  })
})
