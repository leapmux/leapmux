import { createMemo, createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { getCRDTBridge, setCRDTBridge } from '~/lib/crdt'
import { emitSplitTile } from '~/stores/layoutOps'
import { seedWorkspace, withTestBridge } from '~/test-support/crdtBridge'
import { createTestLayoutStore } from '~/test-support/tabStores'

describe('createLayoutStore (projection-driven)', () => {
  it('renders the seeded root tile from the CRDT projection', () => {
    withTestBridge((_harness) => {
      const store = createTestLayoutStore()
      expect(store.state.root.type).toBe('leaf')
      expect(store.state.root.id).toBe('root-leaf')
      expect(store.getAllTileIds()).toEqual(['root-leaf'])
    }, { rootTileId: 'root-leaf' })
  })

  it('splitTile emits a 9-op batch that flips T LEAF→SPLIT in the projection', () => {
    withTestBridge((harness) => {
      const store = createTestLayoutStore()
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
      const store = createTestLayoutStore()
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
      const store = createTestLayoutStore()
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
      const store = createTestLayoutStore()
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
      const store = createTestLayoutStore()
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
      const store = createTestLayoutStore()
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
        return op.body.case ?? ''
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
      const store = createTestLayoutStore()
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

/**
 * Two accessors the cross-workspace move depends on, both reading a workspace
 * OTHER than the one this store projects.
 */
describe('cross-workspace accessors', () => {
  /**
   * `focusedTileIdFor` returns the raw stored pointer, which has no liveness
   * guarantee: `useFocusInvariant` repairs only the ACTIVE workspace, so
   * another workspace's remembered tile can name one another client has since
   * closed. Feeding that to `tile_id` is a batch the hub rejects, which
   * silently reverts the user's drag. `focusedLeafIdFor` is what makes the
   * validation unforgettable instead of a caller's duty.
   */
  describe('focusedLeafIdFor', () => {
    it('returns null for a remembered tile that is no longer in the tree', () => {
      withTestBridge(() => {
        createRoot((dispose) => {
          const store = createTestLayoutStore()
          store.setFocusedTile('tile-closed-by-another-client', 'ws-test')

          expect(store.focusedTileIdFor('ws-test'), 'the raw pointer still holds it')
            .toBe('tile-closed-by-another-client')
          expect(store.focusedLeafIdFor('ws-test'), 'but the validated read refuses it').toBeNull()
          dispose()
        })
      }, { workspaceId: 'ws-test', rootTileId: 'root-leaf' })
    })

    it('returns the tile when it IS a live leaf', () => {
      withTestBridge(() => {
        createRoot((dispose) => {
          const store = createTestLayoutStore()
          store.setFocusedTile('root-leaf', 'ws-test')
          expect(store.focusedLeafIdFor('ws-test')).toBe('root-leaf')
          dispose()
        })
      }, { workspaceId: 'ws-test', rootTileId: 'root-leaf' })
    })

    it('returns null when nothing has been focused', () => {
      withTestBridge(() => {
        createRoot((dispose) => {
          const store = createTestLayoutStore()
          expect(store.focusedLeafIdFor('ws-test')).toBeNull()
          dispose()
        })
      }, { workspaceId: 'ws-test', rootTileId: 'root-leaf' })
    })

    it('refuses a tile that is a SPLIT rather than a leaf', () => {
      withTestBridge(() => {
        createRoot((dispose) => {
          const store = createTestLayoutStore()
          // Splitting turns `root-leaf` into a SPLIT; `tile_id` may only name a
          // leaf, and the hub rejects anything else.
          store.splitTile('root-leaf', 'horizontal')
          store.setFocusedTile('root-leaf', 'ws-test')
          expect(store.focusedLeafIdFor('ws-test')).toBeNull()
          dispose()
        })
      }, { workspaceId: 'ws-test', rootTileId: 'root-leaf' })
    })
  })

  /**
   * `tileOrderFor` must hand back the SAME array while the order is unchanged:
   * `project()` allocates a fresh `mainTree` per call, so a new array every
   * time would invalidate `WorkspaceTabTree`'s `buildTree` memo for every
   * sidebar row on every CRDT tick.
   */
  describe('tileOrderFor', () => {
    it('preserves array identity while the order is unchanged', () => {
      withTestBridge(() => {
        createRoot((dispose) => {
          const store = createTestLayoutStore()
          const first = store.tileOrderFor('ws-test')
          expect(first).toEqual(['root-leaf'])
          expect(store.tileOrderFor('ws-test'), 'same reference, or the sidebar memo re-runs each tick')
            .toBe(first)
          dispose()
        })
      }, { workspaceId: 'ws-test', rootTileId: 'root-leaf' })
    })

    it('hands back a new array once the order actually changes', () => {
      withTestBridge(() => {
        createRoot((dispose) => {
          const store = createTestLayoutStore()
          const before = store.tileOrderFor('ws-test')
          store.splitTile('root-leaf', 'horizontal')
          const after = store.tileOrderFor('ws-test')
          expect(after).not.toBe(before)
          expect(after.length, 'the split produced two leaves').toBe(2)
          dispose()
        })
      }, { workspaceId: 'ws-test', rootTileId: 'root-leaf' })
    })

    // Entries for workspaces that leave the projection are dropped, so this
    // cannot grow one row per workspace ever viewed. The sweep has moved twice:
    // it first sat BELOW two early returns that each skipped exactly the case
    // it exists for, and then rode inside `tileOrderFor` -- a read accessor, so
    // it both wrote a signal mid-render and subscribed every sidebar row's
    // tile-order memo to the focus map. It is now a projection-gated effect
    // (`useLayoutFocusSweep`), like its two siblings.
    it('drops cached rows and remembered focus for a workspace that left the projection', () => {
      withTestBridge((harness) => {
        createRoot((dispose) => {
          seedWorkspace(harness, 'ws-doomed', 'doomed-tile')
          const store = createTestLayoutStore()

          expect(store.tileOrderFor('ws-doomed')).toEqual(['doomed-tile'])
          store.setFocusedTile('doomed-tile', 'ws-doomed')
          expect(store.focusedTileIdFor('ws-doomed')).toBe('doomed-tile')

          delete harness.pending.state.confirmedState.workspaces['ws-doomed']
          harness.pending.recomputeSpeculative()
          // Any real op re-derives the projection, the way a live client would
          // see the deletion land. `recomputeSpeculative` alone mutates state
          // in place without notifying, so the memo would still hold the old
          // projection and the sweep would have nothing to act on.
          emitSplitTile(getCRDTBridge()!, 'root-leaf', 'horizontal')

          // The sweep is a projection-gated effect now, not a side effect of a
          // read accessor: nothing has to ASK for a tile order for the entry to
          // go. That is the point -- the deleted workspace is gone from the
          // sidebar, so nothing would ask.
          store.retainFocusOnly(new Set(['ws-test']))

          expect(store.focusedTileIdFor('ws-doomed'), 'the focus entry must not outlive the workspace')
            .toBeNull()
          dispose()
        })
      }, { workspaceId: 'ws-test', rootTileId: 'root-leaf' })
    })

    it('returns empty for a workspace the projection does not have', () => {
      withTestBridge(() => {
        createRoot((dispose) => {
          const store = createTestLayoutStore()
          expect(store.tileOrderFor('ws-never-existed')).toEqual([])
          dispose()
        })
      }, { workspaceId: 'ws-test', rootTileId: 'root-leaf' })
    })
  })
})

/**
 * The tile-order read must not be coupled to remembered focus.
 *
 * `tileOrderFor` is called during render for EVERY sidebar row
 * (`WorkspaceSectionContent` -> `WorkspaceTabTree`'s `tileOrderProjection`
 * memo). While the focus sweep lived inside it, the accessor read
 * `focusByWorkspace()`, so every `setFocusedTile` -- every tab click, tile
 * click and drag -- invalidated the tile order of every workspace row at once.
 */
describe('tileOrderFor reactivity', () => {
  it('does not subscribe a tile-order reader to remembered focus', () => {
    withTestBridge(() => {
      createRoot((dispose) => {
        const store = createTestLayoutStore()
        let runs = 0
        const order = createMemo(() => {
          runs++
          return store.tileOrderFor('ws-test')
        })
        order()
        expect(runs).toBe(1)

        store.setFocusedTile('root-leaf', 'ws-test')
        order()

        expect(runs, 'a focus write must not invalidate a tile-order read').toBe(1)
        dispose()
      })
    }, { workspaceId: 'ws-test', rootTileId: 'root-leaf' })
  })
})
