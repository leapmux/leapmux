import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { useTileDragDrop } from '~/components/shell/useTileDragDrop'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { setCRDTBridge } from '~/lib/crdt'
import { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import { createLayoutStore } from '~/stores/layout.store'
import { tabKey } from '~/stores/tab.helpers'
import { createTabStore } from '~/stores/tab.store'
import { installTestBridge } from '~/test-support/crdtBridge'

afterEach(() => setCRDTBridge(null))

// Regression: dragging the active tab across tiles used to leave the
// focused tile pointed at the source — so after dropping the agent
// onto a sibling tile, the user was still "in" the (now-empty or
// stale) source tile. handleCrossTileMove now captures
// `wasActiveOnSource` BEFORE the move and, when true, follows the
// dragged tab to its new tile via `layoutStore.setFocusedTile`.
//
// Inverse: dragging an inactive tab (user is reading tab X, dragging
// tab Y) must NOT steal focus — the user's attention is still on X.
describe('useTileDragDrop.handleCrossTileMove focus follows active tab', () => {
  it('moves focusedTileId to the destination when the dragged tab was active on the source', () => {
    createRoot((dispose) => {
      installTestBridge({ rootTileId: 'root-leaf' })
      const tabStore = createTabStore()
      const layoutStore = createLayoutStore()
      const floatingWindowStore = createFloatingWindowStore()

      const otherTileId = layoutStore.splitTile('root-leaf', 'horizontal')!
      const [tileA, tileB] = layoutStore.getAllTileIds()
      const fromTile = tileB === otherTileId ? tileA : tileB
      const toTile = otherTileId

      tabStore.addTab({ type: TabType.AGENT, id: 'a-active', tileId: fromTile, workerId: 'w-1' })
      tabStore.setActiveTabForTile(fromTile, TabType.AGENT, 'a-active')
      // Add a sibling tab on the destination so the drop has context.
      tabStore.addTab({ type: TabType.AGENT, id: 'a-dest', tileId: toTile, workerId: 'w-1' }, { activate: false })
      layoutStore.setFocusedTile(fromTile)

      const ops = useTileDragDrop({ tabStore, layoutStore, floatingWindowStore })
      const draggedKey = tabKey({ type: TabType.AGENT, id: 'a-active' })
      ops.handleCrossTileMove(fromTile, toTile, draggedKey, null)

      expect(layoutStore.focusedTileId()).toBe(toTile)
      dispose()
    })
  })

  it('leaves focusedTileId alone when the dragged tab was not active on the source', () => {
    createRoot((dispose) => {
      installTestBridge({ rootTileId: 'root-leaf' })
      const tabStore = createTabStore()
      const layoutStore = createLayoutStore()
      const floatingWindowStore = createFloatingWindowStore()

      const otherTileId = layoutStore.splitTile('root-leaf', 'horizontal')!
      const [tileA, tileB] = layoutStore.getAllTileIds()
      const fromTile = tileB === otherTileId ? tileA : tileB
      const toTile = otherTileId

      // Two tabs on the source: a-active is the visible one; the
      // user is dragging the inactive a-bg without leaving their
      // current attention.
      tabStore.addTab({ type: TabType.AGENT, id: 'a-active', tileId: fromTile, workerId: 'w-1' })
      tabStore.addTab({ type: TabType.AGENT, id: 'a-bg', tileId: fromTile, workerId: 'w-1' }, { activate: false })
      tabStore.setActiveTabForTile(fromTile, TabType.AGENT, 'a-active')
      layoutStore.setFocusedTile(fromTile)

      const ops = useTileDragDrop({ tabStore, layoutStore, floatingWindowStore })
      const draggedKey = tabKey({ type: TabType.AGENT, id: 'a-bg' })
      ops.handleCrossTileMove(fromTile, toTile, draggedKey, null)

      // Focus stays on the source — the active tab is unchanged on
      // that tile and the user's attention should follow it.
      expect(layoutStore.focusedTileId()).toBe(fromTile)
      dispose()
    })
  })
})
