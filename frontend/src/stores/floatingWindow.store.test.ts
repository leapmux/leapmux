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

  it('updateSize updates width and height with minimum enforcement', () => {
    withStore((store) => {
      const { windowId } = store.addWindow()
      store.updateSize(windowId, 0.8, 0.9)
      const win = store.getWindow(windowId)!
      expect(win.width).toBe(0.8)
      expect(win.height).toBe(0.9)

      // Minimum size enforcement
      store.updateSize(windowId, 0.01, 0.01)
      const win2 = store.getWindow(windowId)!
      expect(win2.width).toBe(0.05)
      expect(win2.height).toBe(0.05)
    })
  })

  it('bringToFront increments zIndex', () => {
    withStore((store) => {
      const { windowId: id1 } = store.addWindow()
      const { windowId: id2 } = store.addWindow()
      const z1Before = store.getWindow(id1)!.zIndex
      const z2Before = store.getWindow(id2)!.zIndex
      expect(z2Before).toBeGreaterThan(z1Before)

      store.bringToFront(id1)
      expect(store.getWindow(id1)!.zIndex).toBeGreaterThan(z2Before)
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

  it('getWindowTileIds returns tile IDs for a specific window', () => {
    withStore((store) => {
      const { windowId, tileId } = store.addWindow()
      const newTileId = store.splitTile(windowId, tileId, 'vertical')!

      const tileIds = store.getWindowTileIds(windowId)
      expect(tileIds).toContain(tileId)
      expect(tileIds).toContain(newTileId)
      expect(tileIds.length).toBe(2)
    })
  })

  it('isWindowEmpty returns true when all tiles have no tabs', () => {
    withStore((store) => {
      const { windowId } = store.addWindow()
      expect(store.isWindowEmpty(windowId, () => [])).toBe(true)
      expect(store.isWindowEmpty(windowId, () => ['tab1'])).toBe(false)
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
})
