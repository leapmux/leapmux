import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createFloatingWindowStore } from './floatingWindow.store'

function withStore(fn: (store: ReturnType<typeof createFloatingWindowStore>) => void) {
  createRoot((dispose) => {
    const store = createFloatingWindowStore()
    fn(store)
    dispose()
  })
}

describe('createFloatingWindowStore', () => {
  it('starts with no windows', () => {
    withStore((store) => {
      expect(store.state.windows).toEqual([])
      expect(store.getAllTileIds()).toEqual([])
    })
  })

  it('addWindow creates a window with a tile', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow()
      expect(windowId).toBeTruthy()
      expect(tileId).toBeTruthy()
      expect(store.state.windows.length).toBe(1)
      const win = store.getWindow(windowId)!
      expect(win).toBeDefined()
      expect(win.layoutRoot).toEqual({ type: 'leaf', id: tileId })
      expect(win.focusedTileId).toBe(tileId)
    })
  })

  it('addWindow accepts position/size options', () => {
    withStore((store) => {
      const { windowId } = store.addWindow({ x: 0.1, y: 0.2, width: 0.6, height: 0.7 })
      const win = store.getWindow(windowId)!
      expect(win.x).toBe(0.1)
      expect(win.y).toBe(0.2)
      expect(win.width).toBe(0.6)
      expect(win.height).toBe(0.7)
    })
  })

  it('addWindow cascades position so consecutive default windows land at distinct spots', () => {
    // Without cascade, every popped-out tab opens at the same default
    // position and stacks invisibly on top of the previous window. This
    // pins the contract that back-to-back `addWindow()` calls produce
    // visibly distinct positions.
    withStore((store) => {
      const { windowId: id1 } = store.addWindow()
      const { windowId: id2 } = store.addWindow()
      const { windowId: id3 } = store.addWindow()
      const w1 = store.getWindow(id1)!
      const w2 = store.getWindow(id2)!
      const w3 = store.getWindow(id3)!
      expect(w2.x).toBeGreaterThan(w1.x)
      expect(w2.y).toBeGreaterThan(w1.y)
      expect(w3.x).toBeGreaterThan(w2.x)
      expect(w3.y).toBeGreaterThan(w2.y)
    })
  })

  it('addWindow does NOT cascade when an explicit position is provided', () => {
    // Callers spawning windows at user-driven coordinates (drop position,
    // restored geometry, etc.) must get exactly what they asked for.
    withStore((store) => {
      store.addWindow() // bumps the count so a cascade would otherwise apply
      const { windowId } = store.addWindow({ x: 0.5, y: 0.5 })
      const win = store.getWindow(windowId)!
      expect(win.x).toBe(0.5)
      expect(win.y).toBe(0.5)
    })
  })

  it('removeWindow removes the window', () => {
    withStore((store) => {
      const { windowId } = store.addWindow()
      store.addWindow()
      expect(store.state.windows.length).toBe(2)
      store.removeWindow(windowId)
      expect(store.state.windows.length).toBe(1)
      expect(store.getWindow(windowId)).toBeUndefined()
    })
  })

  it('updatePosition updates x and y', () => {
    withStore((store) => {
      const { windowId } = store.addWindow()
      store.updatePosition(windowId, 0.5, 0.6)
      const win = store.getWindow(windowId)!
      expect(win.x).toBe(0.5)
      expect(win.y).toBe(0.6)
    })
  })

  it('updateGeometry clamps width and height to the minimum', () => {
    withStore((store) => {
      const { windowId } = store.addWindow()
      const win0 = store.getWindow(windowId)!
      store.updateGeometry(windowId, win0.x, win0.y, 0.8, 0.9)
      const win = store.getWindow(windowId)!
      expect(win.width).toBe(0.8)
      expect(win.height).toBe(0.9)

      store.updateGeometry(windowId, win.x, win.y, 0.01, 0.01)
      const win2 = store.getWindow(windowId)!
      expect(win2.width).toBe(0.05)
      expect(win2.height).toBe(0.05)
    })
  })

  it('addWindow appends so the new window is topmost (last in array)', () => {
    withStore((store) => {
      const { windowId: id1 } = store.addWindow()
      const { windowId: id2 } = store.addWindow()
      // Last-in-array is topmost by store contract.
      expect(store.state.windows.map(w => w.id)).toEqual([id1, id2])
    })
  })

  it('bringToFront moves the window to the end of the array', () => {
    withStore((store) => {
      const { windowId: id1 } = store.addWindow()
      const { windowId: id2 } = store.addWindow()
      expect(store.state.windows.map(w => w.id)).toEqual([id1, id2])

      store.bringToFront(id1)
      expect(store.state.windows.map(w => w.id)).toEqual([id2, id1])
    })
  })

  it('bringToFront is a no-op when already topmost', () => {
    withStore((store) => {
      const { windowId: id1 } = store.addWindow()
      const { windowId: id2 } = store.addWindow()
      const before = store.state.windows
      store.bringToFront(id2)
      // No mutation: same array reference, same order.
      expect(store.state.windows).toBe(before)
      expect(store.state.windows.map(w => w.id)).toEqual([id1, id2])
    })
  })

  it('setFocusedTile updates the focused tile', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow()
      expect(store.getWindow(windowId)!.focusedTileId).toBe(tileId)
      store.setFocusedTile(windowId, 'other-tile')
      expect(store.getWindow(windowId)!.focusedTileId).toBe('other-tile')
    })
  })

  it('splitTile horizontal creates a split layout', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow()
      const newTileId = store.splitTile(windowId, tileId, 'horizontal')
      expect(newTileId).toBeTruthy()

      const win = store.getWindow(windowId)!
      expect(win.layoutRoot.type).toBe('split')
      if (win.layoutRoot.type === 'split') {
        expect(win.layoutRoot.direction).toBe('horizontal')
        expect(win.layoutRoot.children.length).toBe(2)
      }
    })
  })

  it('splitTile vertical creates a split layout', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow()
      const newTileId = store.splitTile(windowId, tileId, 'vertical')
      expect(newTileId).toBeTruthy()

      const win = store.getWindow(windowId)!
      expect(win.layoutRoot.type).toBe('split')
      if (win.layoutRoot.type === 'split') {
        expect(win.layoutRoot.direction).toBe('vertical')
        expect(win.layoutRoot.children.length).toBe(2)
      }
    })
  })

  it('closeTile removes a tile from a split and optimizes', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow()
      const newTileId = store.splitTile(windowId, tileId, 'horizontal')!

      // Close the new tile — should collapse back to a leaf
      store.closeTile(windowId, newTileId)
      const win = store.getWindow(windowId)!
      expect(win.layoutRoot.type).toBe('leaf')
      expect(win.layoutRoot.id).toBe(tileId)
    })
  })

  it('closeTile on last tile removes the window', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow()
      store.closeTile(windowId, tileId)
      expect(store.getWindow(windowId)).toBeUndefined()
      expect(store.state.windows.length).toBe(0)
    })
  })

  it('closeTile updates focusedTileId when focused tile is closed', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow()
      const newTileId = store.splitTile(windowId, tileId, 'horizontal')!
      store.setFocusedTile(windowId, newTileId)

      store.closeTile(windowId, newTileId)
      const win = store.getWindow(windowId)!
      expect(win.focusedTileId).toBe(tileId)
    })
  })

  describe('closeTile discriminated return', () => {
    it('returns { kind: "changed" } when the tile is removed but the window remains', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const newTileId = store.splitTile(windowId, tileId, 'horizontal')!
        const result = store.closeTile(windowId, newTileId)
        expect(result).toEqual({ kind: 'changed' })
      })
    })

    it('returns { kind: "disposed", tileIds } with the about-to-disappear leaves when the last tile closes', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const result = store.closeTile(windowId, tileId)
        expect(result.kind).toBe('disposed')
        if (result.kind === 'disposed')
          expect([...result.tileIds]).toEqual([tileId])
      })
    })

    it('disposed result includes every leaf that was in the multi-tile window before drop', () => {
      // Reproduces the bug we set out to prevent: the only way to learn
      // which tab-store entries to scrub after the window disappears is
      // for `closeTile` to surface the tile-ids it just disposed. Before
      // the discriminated return, callers had to pre-snapshot via
      // `getWindowTileIdSet` — this test pins the no-pre-snapshot contract.
      withStore((store) => {
        const { windowId, tileId: t1 } = store.addWindow()
        const t2 = store.splitTile(windowId, t1, 'horizontal')!
        const t3 = store.splitTile(windowId, t2, 'vertical')!
        // Close all three, last one disposes.
        store.closeTile(windowId, t1)
        store.closeTile(windowId, t2)
        const result = store.closeTile(windowId, t3)
        expect(result.kind).toBe('disposed')
        if (result.kind === 'disposed')
          expect([...result.tileIds]).toEqual([t3])
      })
    })

    it('returns { kind: "noop" } when the window has already been disposed', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        // First call disposes the window…
        store.closeTile(windowId, tileId)
        // …second call against the disposed window must not throw.
        expect(store.closeTile(windowId, tileId)).toEqual({ kind: 'noop' })
      })
    })
  })

  it('getAllTileIds returns all tile IDs across all windows', () => {
    withStore((store) => {
      const { tileId: t1 } = store.addWindow()
      const { windowId: w2, tileId: t2 } = store.addWindow()
      const t3 = store.splitTile(w2, t2, 'horizontal')!

      const allTiles = store.getAllTileIds()
      expect(allTiles).toContain(t1)
      expect(allTiles).toContain(t2)
      expect(allTiles).toContain(t3)
      expect(allTiles.length).toBe(3)
    })
  })

  it('getWindowForTile finds the correct window', () => {
    withStore((store) => {
      const { windowId: w1, tileId: t1 } = store.addWindow()
      const { windowId: w2, tileId: t2 } = store.addWindow()

      expect(store.getWindowForTile(t1)).toBe(w1)
      expect(store.getWindowForTile(t2)).toBe(w2)
      expect(store.getWindowForTile('nonexistent')).toBeNull()
    })
  })

  it('getWindowTileIdSet returns tile IDs for a specific window', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow()
      const newTileId = store.splitTile(windowId, tileId, 'vertical')!

      const tileIds = store.getWindowTileIdSet(windowId)
      expect(tileIds).not.toBeNull()
      expect(tileIds!.has(tileId)).toBe(true)
      expect(tileIds!.has(newTileId)).toBe(true)
      expect(tileIds!.size).toBe(2)
    })
  })

  it('updateRatios updates split ratios', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow()
      store.splitTile(windowId, tileId, 'horizontal')

      const win = store.getWindow(windowId)!
      if (win.layoutRoot.type === 'split') {
        const splitId = win.layoutRoot.id!
        store.updateRatios(windowId, splitId, [0.3, 0.7])

        const updated = store.getWindow(windowId)!
        if (updated.layoutRoot.type === 'split') {
          expect(updated.layoutRoot.ratios).toEqual([0.3, 0.7])
        }
      }
    })
  })

  it('snapshot and restore round-trips state', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow({ x: 0.1, y: 0.2 })
      store.splitTile(windowId, tileId, 'horizontal')

      const snap = store.snapshot()
      expect(snap.windows.length).toBe(1)
      expect(snap.windows[0].x).toBe(0.1)

      // Mutate the store
      store.addWindow()
      expect(store.state.windows.length).toBe(2)

      // Restore
      store.restore(snap)
      expect(store.state.windows.length).toBe(1)
      expect(store.state.windows[0].x).toBe(0.1)
    })
  })

  it('toProto and fromProto round-trips via proto format', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow({ x: 0.3, y: 0.4, width: 0.5, height: 0.6 })
      store.splitTile(windowId, tileId, 'horizontal')

      const protos = store.toProto()
      expect(protos.length).toBe(1)
      expect(protos[0].x).toBe(0.3)
      expect(protos[0].y).toBe(0.4)
      expect(protos[0].width).toBe(0.5)
      expect(protos[0].height).toBe(0.6)
      expect(protos[0].layout).toBeDefined()

      // Restore from proto in a new store
      withStore((store2) => {
        store2.fromProto(protos)
        expect(store2.state.windows.length).toBe(1)
        const win = store2.state.windows[0]
        expect(win.x).toBe(0.3)
        expect(win.y).toBe(0.4)
        expect(win.width).toBe(0.5)
        expect(win.height).toBe(0.6)
        expect(win.layoutRoot.type).toBe('split')
      })
    })
  })

  it('splitTile adds sibling in same direction split', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow()
      const t2 = store.splitTile(windowId, tileId, 'horizontal')!
      // Split t2 in the same direction — should add a third child
      const t3 = store.splitTile(windowId, t2, 'horizontal')!
      expect(t3).toBeTruthy()

      const win = store.getWindow(windowId)!
      if (win.layoutRoot.type === 'split') {
        expect(win.layoutRoot.children.length).toBe(3)
        expect(win.layoutRoot.direction).toBe('horizontal')
      }
    })
  })

  it('makeGrid wraps a leaf into a grid scoped to one window', () => {
    withStore((store) => {
      const { windowId: w1, tileId: t1 } = store.addWindow()
      const { windowId: w2, tileId: t2 } = store.addWindow()

      const result = store.makeGrid(w1, t1, 2, 2)
      expect(result).not.toBeNull()
      expect(result!.cellTileIds.length).toBe(4)

      const win1 = store.getWindow(w1)!
      expect(win1.layoutRoot.type).toBe('grid')
      // The other window is untouched.
      const win2 = store.getWindow(w2)!
      expect(win2.layoutRoot).toEqual({ type: 'leaf', id: t2 })
    })
  })

  it('removeGrid replaces a root grid with a fresh fwtile leaf', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow()
      const result = store.makeGrid(windowId, tileId, 2, 2)!

      store.removeGrid(windowId, result.gridId)
      const win = store.getWindow(windowId)!
      expect(win.layoutRoot.type).toBe('leaf')
      if (win.layoutRoot.type === 'leaf') {
        expect(win.layoutRoot.id).toMatch(/^fwtile-/)
        expect(win.layoutRoot.id).not.toBe(tileId)
      }
    })
  })

  it('replaceGridWithLeaf returns a fwtile-prefixed id and reroots focus', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow()
      const result = store.makeGrid(windowId, tileId, 2, 2)!
      store.setFocusedTile(windowId, result.cellTileIds[3])

      const newId = store.replaceGridWithLeaf(windowId, result.gridId)
      expect(newId).toMatch(/^fwtile-/)
      const win = store.getWindow(windowId)!
      if (win.layoutRoot.type === 'leaf') {
        expect(win.layoutRoot.id).toBe(newId)
      }
      expect(win.focusedTileId).toBe(newId)
    })
  })

  it('toProto and fromProto round-trip a grid in a floating window', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow({ x: 0.1, y: 0.2, width: 0.3, height: 0.4 })
      store.makeGrid(windowId, tileId, 1, 2)

      const protos = store.toProto()
      withStore((store2) => {
        store2.fromProto(protos)
        const win = store2.state.windows[0]
        expect(win.x).toBe(0.1)
        expect(win.layoutRoot.type).toBe('grid')
        if (win.layoutRoot.type === 'grid') {
          expect(win.layoutRoot.rows).toBe(1)
          expect(win.layoutRoot.cols).toBe(2)
        }
      })
    })
  })

  describe('owner(windowId)', () => {
    it('returns a LayoutOwner with all required methods', () => {
      withStore((store) => {
        const { windowId } = store.addWindow()
        const owner = store.owner(windowId)
        expect(typeof owner.collectTileIdsInGrid).toBe('function')
        expect(typeof owner.splitTile).toBe('function')
        expect(typeof owner.makeGrid).toBe('function')
        expect(typeof owner.removeGrid).toBe('function')
        expect(typeof owner.replaceGridWithLeaf).toBe('function')
      })
    })

    it('owner reads are lazy: an owner held across mutations sees fresh state', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const owner = store.owner(windowId) // captured before any mutation
        const newTileId = store.splitTile(windowId, tileId, 'horizontal')!
        const result = store.makeGrid(windowId, newTileId, 2, 2)!
        // Mutations observed through the owner without re-fetching it.
        expect(owner.collectTileIdsInGrid(result.gridId)).toHaveLength(4)
      })
    })

    it('splitTile delegates to the per-window splitTile', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        store.owner(windowId).splitTile(tileId, 'horizontal')
        const win = store.getWindow(windowId)!
        expect(win.layoutRoot.type).toBe('split')
      })
    })

    it('makeGrid delegates and produces a grid in the right window', () => {
      withStore((store) => {
        const { windowId: w1, tileId: t1 } = store.addWindow()
        const { windowId: w2 } = store.addWindow()
        store.owner(w1).makeGrid(t1, 2, 2)
        expect(store.getWindow(w1)!.layoutRoot.type).toBe('grid')
        // Other window is untouched.
        expect(store.getWindow(w2)!.layoutRoot.type).toBe('leaf')
      })
    })

    it('removeGrid is a no-op when the window has been removed', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const { gridId } = store.makeGrid(windowId, tileId, 2, 2)!
        const owner = store.owner(windowId)
        store.removeWindow(windowId)
        // Should not throw despite the missing window.
        expect(() => owner.removeGrid(gridId)).not.toThrow()
      })
    })

    it('replaceGridWithLeaf returns the new tile id and replaces the grid', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const { gridId } = store.makeGrid(windowId, tileId, 2, 2)!
        const newTileId = store.owner(windowId).replaceGridWithLeaf(gridId)
        expect(newTileId).toBeTruthy()
        const win = store.getWindow(windowId)!
        expect(win.layoutRoot.type).toBe('leaf')
        expect((win.layoutRoot as { id: string }).id).toBe(newTileId)
      })
    })

    it('returns a stable reference per windowId, distinct across windows, surviving structural mutations', () => {
      withStore((store) => {
        const { windowId: w1, tileId: t1 } = store.addWindow()
        const { windowId: w2 } = store.addWindow()

        const ownerA = store.owner(w1)
        const ownerB = store.owner(w1)
        // Same window → same reference.
        expect(ownerB).toBe(ownerA)
        // Different window → distinct reference.
        expect(store.owner(w2)).not.toBe(ownerA)

        // Reference survives a layout mutation in the same window.
        store.splitTile(w1, t1, 'horizontal')
        expect(store.owner(w1)).toBe(ownerA)
      })
    })

    it('drops the cached owner when its window is removed and recreates a fresh one for a new window of the same id space', () => {
      withStore((store) => {
        const { windowId } = store.addWindow()
        const before = store.owner(windowId)
        store.removeWindow(windowId)
        // After removal, the cache no longer holds the owner; the fallback
        // path returns a freshly-built one whose methods all guard with
        // `findWindow` and degrade to no-op.
        const after = store.owner(windowId)
        expect(after).not.toBe(before)
        expect(after.firstLeafId()).toBeNull()
      })
    })
  })

  // The reverse index `tileId → windowId` is maintained alongside
  // `state.windows` so `getWindowForTile` is O(1) instead of O(W·T). Every
  // mutation that changes which tiles belong to which window must keep the
  // index in sync — these tests exercise each such mutation and assert the
  // index returns the right window (or null) after each.
  describe('getWindowForTile reverse-index', () => {
    it('addWindow inserts the new tile', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        expect(store.getWindowForTile(tileId)).toBe(windowId)
      })
    })

    it('removeWindow drops every tile from the removed window', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const splitTileId = store.splitTile(windowId, tileId, 'horizontal')!
        const { windowId: keptWindowId, tileId: keptTileId } = store.addWindow()

        store.removeWindow(windowId)

        expect(store.getWindowForTile(tileId)).toBeNull()
        expect(store.getWindowForTile(splitTileId)).toBeNull()
        expect(store.getWindowForTile(keptTileId)).toBe(keptWindowId)
      })
    })

    it('splitTile registers the new sibling tile under the same window', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const newTileId = store.splitTile(windowId, tileId, 'horizontal')!
        expect(store.getWindowForTile(tileId)).toBe(windowId)
        expect(store.getWindowForTile(newTileId)).toBe(windowId)
      })
    })

    it('closeTile (non-last) removes the closed tile but keeps siblings', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const newTileId = store.splitTile(windowId, tileId, 'horizontal')!
        store.closeTile(windowId, newTileId)
        expect(store.getWindowForTile(newTileId)).toBeNull()
        expect(store.getWindowForTile(tileId)).toBe(windowId)
      })
    })

    it('closeTile on the last tile removes the tile and the window', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        store.closeTile(windowId, tileId)
        expect(store.getWindowForTile(tileId)).toBeNull()
        expect(store.getWindow(windowId)).toBeUndefined()
      })
    })

    it('makeGrid registers every cell tile (original tileId reused as cells[0,0])', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const result = store.makeGrid(windowId, tileId, 2, 2)!
        // makeGridInTree reuses the original tileId as cells[0,0] and mints
        // fresh ids for the remaining cells; all cell ids must be in the
        // index pointing at the same window.
        expect(result.cellTileIds[0]).toBe(tileId)
        for (const cellId of result.cellTileIds)
          expect(store.getWindowForTile(cellId)).toBe(windowId)
      })
    })

    it('removeGrid drops every old cell tile and registers the replacement leaf', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const result = store.makeGrid(windowId, tileId, 2, 2)!

        store.removeGrid(windowId, result.gridId)

        for (const cellId of result.cellTileIds)
          expect(store.getWindowForTile(cellId)).toBeNull()
        // The replacement leaf is reachable via the new layoutRoot id.
        const win = store.getWindow(windowId)!
        if (win.layoutRoot.type !== 'leaf')
          throw new Error('expected leaf root')
        expect(store.getWindowForTile(win.layoutRoot.id)).toBe(windowId)
      })
    })

    it('replaceGridWithLeaf drops cell tile ids and registers the new tile', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const result = store.makeGrid(windowId, tileId, 2, 2)!
        const newTileId = store.replaceGridWithLeaf(windowId, result.gridId)!

        for (const cellId of result.cellTileIds)
          expect(store.getWindowForTile(cellId)).toBeNull()
        expect(store.getWindowForTile(newTileId)).toBe(windowId)
      })
    })

    it('removeIfEmpty drops tiles when the window is auto-disposed', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        store.removeIfEmpty(windowId, () => [])
        expect(store.getWindowForTile(tileId)).toBeNull()
      })
    })

    it('removeIfEmpty leaves the index untouched when the window is not empty', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        store.removeIfEmpty(windowId, () => ['t'])
        expect(store.getWindowForTile(tileId)).toBe(windowId)
      })
    })

    it('removeIfEmpty does not dispose multi-tile windows even when every tile is empty', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const otherTileId = store.splitTile(windowId, tileId, 'horizontal')!
        // Both tiles empty, but the window has 2 tiles → keep it.
        const removed = store.removeIfEmpty(windowId, () => [])
        expect(removed).toBe(false)
        expect(store.getWindowForTile(tileId)).toBe(windowId)
        expect(store.getWindowForTile(otherTileId)).toBe(windowId)
      })
    })

    it('removeIfEmpty calls onRemoved with the disposed tile id', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        let removed: string | undefined
        store.removeIfEmpty(windowId, () => [], (id) => {
          removed = id
        })
        expect(removed).toBe(tileId)
      })
    })

    it('fromProto rebuilds the index from the loaded layout', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const newTileId = store.splitTile(windowId, tileId, 'horizontal')!
        const protos = store.toProto()

        withStore((store2) => {
          store2.fromProto(protos)
          // Tile ids are preserved across proto round-trip.
          expect(store2.getWindowForTile(tileId)).toBe(store2.state.windows[0].id)
          expect(store2.getWindowForTile(newTileId)).toBe(store2.state.windows[0].id)
        })
      })
    })

    it('restore rebuilds the index from a snapshot', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const newTileId = store.splitTile(windowId, tileId, 'horizontal')!
        const snap = store.snapshot()

        // Mutate, then restore.
        store.closeTile(windowId, newTileId)
        store.restore(snap)

        expect(store.getWindowForTile(tileId)).toBe(windowId)
        expect(store.getWindowForTile(newTileId)).toBe(windowId)
      })
    })

    it('two windows: getWindowForTile returns the right window for each tile', () => {
      withStore((store) => {
        const { windowId: w1, tileId: t1 } = store.addWindow()
        const { windowId: w2, tileId: t2 } = store.addWindow()
        const t1b = store.splitTile(w1, t1, 'horizontal')!
        const t2b = store.splitTile(w2, t2, 'vertical')!

        expect(store.getWindowForTile(t1)).toBe(w1)
        expect(store.getWindowForTile(t1b)).toBe(w1)
        expect(store.getWindowForTile(t2)).toBe(w2)
        expect(store.getWindowForTile(t2b)).toBe(w2)
      })
    })

    it('returns null for tile ids that never existed', () => {
      withStore((store) => {
        store.addWindow()
        expect(store.getWindowForTile('nonexistent')).toBeNull()
      })
    })

    it('nested grid cell tiles are all indexed', () => {
      // Cells inside a grid are leaves with their own ids — they all need
      // to be reachable via getWindowForTile, not just the grid root.
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const result = store.makeGrid(windowId, tileId, 2, 3)!
        expect(result.cellTileIds).toHaveLength(6)
        for (const cellId of result.cellTileIds)
          expect(store.getWindowForTile(cellId)).toBe(windowId)
      })
    })
  })

  describe('updateOpacity', () => {
    // The wheel handler in `FloatingWindowContainer` fires this ~60×/sec on a
    // free-spinning wheel. Returning a boolean lets the caller skip the
    // (debounced) `persistLayout` notify when the value is pinned at a clamp
    // — without this, every tick at opacity=1 would still bounce the persist
    // pipeline.
    it('returns true when the opacity actually changes', () => {
      withStore((store) => {
        const { windowId } = store.addWindow()
        expect(store.updateOpacity(windowId, 0.6)).toBe(true)
        expect(store.getWindow(windowId)!.opacity).toBe(0.6)
      })
    })

    it('returns false when the clamped value is unchanged', () => {
      withStore((store) => {
        const { windowId } = store.addWindow()
        // Default opacity is 1 — wheel-up clamps back to 1.
        expect(store.updateOpacity(windowId, 1.5)).toBe(false)
        // Drop to floor, then attempt to go below — clamp pins at 0.2.
        expect(store.updateOpacity(windowId, 0.2)).toBe(true)
        expect(store.updateOpacity(windowId, 0.1)).toBe(false)
        expect(store.updateOpacity(windowId, 0)).toBe(false)
        expect(store.getWindow(windowId)!.opacity).toBe(0.2)
      })
    })

    it('returns false when the windowId is unknown', () => {
      withStore((store) => {
        expect(store.updateOpacity('missing', 0.5)).toBe(false)
      })
    })

    it('returns false on a redundant write (same value)', () => {
      withStore((store) => {
        const { windowId } = store.addWindow()
        store.updateOpacity(windowId, 0.7)
        expect(store.updateOpacity(windowId, 0.7)).toBe(false)
      })
    })
  })

  describe('getWindowForTile after structural changes', () => {
    // Regression for the inverse `tileToWindowId` memo: removing a window
    // (or moving its tile) must not leave stale tileId → windowId entries.
    it('returns null after the owning window is removed', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        expect(store.getWindowForTile(tileId)).toBe(windowId)
        store.removeWindow(windowId)
        expect(store.getWindowForTile(tileId)).toBeNull()
      })
    })

    it('reflects new tile ids after splitTile', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const newTileId = store.splitTile(windowId, tileId, 'horizontal')!
        expect(store.getWindowForTile(tileId)).toBe(windowId)
        expect(store.getWindowForTile(newTileId)).toBe(windowId)
      })
    })

    it('drops the entry for a tile that closeTile removes', () => {
      withStore((store) => {
        const { windowId, tileId } = store.addWindow()
        const newTileId = store.splitTile(windowId, tileId, 'horizontal')!
        store.closeTile(windowId, newTileId)
        expect(store.getWindowForTile(newTileId)).toBeNull()
        expect(store.getWindowForTile(tileId)).toBe(windowId)
      })
    })
  })
})
