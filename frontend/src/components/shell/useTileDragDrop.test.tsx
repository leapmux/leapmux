import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { useTileDragDrop } from '~/components/shell/useTileDragDrop'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { setCRDTBridge } from '~/lib/crdt'
import { tabKey } from '~/stores/tab.helpers'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestFloatingWindowStore, createTestTabStores } from '~/test-support/tabStores'

afterEach(() => setCRDTBridge(null))

/**
 * Set up two sibling tiles under the seeded root and return both ids.
 * `splitTile` returns the NEW leaf; the other one keeps the source's tabs.
 */
function twoTiles(layoutStore: ReturnType<typeof createTestTabStores>['layoutStore']) {
  const toTile = layoutStore.splitTile('root-leaf', 'horizontal')!
  const [tileA, tileB] = layoutStore.getAllTileIds()
  return { fromTile: tileB === toTile ? tileA : tileB, toTile }
}

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
      const { view, selection, layoutStore } = createTestTabStores('ws-test')
      const floatingWindowStore = createTestFloatingWindowStore()
      const { fromTile, toTile } = twoTiles(layoutStore)

      emitAddTab({ type: TabType.AGENT, id: 'a-active', tileId: fromTile, position: 'a', workerId: 'w-1' })
      selection.setActiveById(TabType.AGENT, 'a-active')
      // A sibling tab on the destination so the drop has context.
      emitAddTab({ type: TabType.AGENT, id: 'a-dest', tileId: toTile, position: 'a', workerId: 'w-1' })
      layoutStore.setFocusedTile(fromTile)

      const ops = useTileDragDrop({ view, selection, layoutStore, floatingWindowStore })
      ops.handleCrossTileMove(fromTile, toTile, tabKey({ type: TabType.AGENT, id: 'a-active' }), null)

      expect(layoutStore.focusedTileId()).toBe(toTile)
      dispose()
    })
  })

  it('leaves focusedTileId alone when the dragged tab was not active on the source', () => {
    createRoot((dispose) => {
      installTestBridge({ rootTileId: 'root-leaf' })
      const { view, selection, layoutStore } = createTestTabStores('ws-test')
      const floatingWindowStore = createTestFloatingWindowStore()
      const { fromTile, toTile } = twoTiles(layoutStore)

      // Two tabs on the source: a-active is the visible one; the
      // user is dragging the inactive a-bg without leaving their
      // current attention.
      emitAddTab({ type: TabType.AGENT, id: 'a-active', tileId: fromTile, position: 'a', workerId: 'w-1' })
      emitAddTab({ type: TabType.AGENT, id: 'a-bg', tileId: fromTile, position: 'b', workerId: 'w-1' })
      selection.setActiveById(TabType.AGENT, 'a-active')
      layoutStore.setFocusedTile(fromTile)

      const ops = useTileDragDrop({ view, selection, layoutStore, floatingWindowStore })
      ops.handleCrossTileMove(fromTile, toTile, tabKey({ type: TabType.AGENT, id: 'a-bg' }), null)

      // Focus stays on the source — the active tab is unchanged on
      // that tile and the user's attention should follow it.
      expect(layoutStore.focusedTileId()).toBe(fromTile)
      dispose()
    })
  })
})

// Regression: `moveTabToTile` applies its op to `speculativeState`
// synchronously, so reading `view.forTile(toTileId)` after the move
// returned a list that ALREADY contained the dragged tab — carrying its
// stale SOURCE position, because only `tile_id` changed. The dragged tab
// then acted as one of its own boundary neighbours and
// `positionAtInsertIdx` minted a rank against itself.
//
// The canonical break is the commonest drop there is: both tiles' first
// tabs share the seed rank `'n'`, so `mid('n','n')` returns `'nn'` — the
// dragged tab lands AFTER its drop target and tied with that target's
// neighbour, instead of before the target.
describe('useTileDragDrop.handleCrossTileMove drop position', () => {
  it('positions the dragged tab against the destination as it was BEFORE the move', () => {
    createRoot((dispose) => {
      installTestBridge({ rootTileId: 'root-leaf' })
      const { view, selection, layoutStore } = createTestTabStores('ws-test')
      const floatingWindowStore = createTestFloatingWindowStore()
      const { fromTile, toTile } = twoTiles(layoutStore)

      // Source holds one tab at the same seed rank the destination's
      // head uses — the collision that makes the stale read observable.
      emitAddTab({ type: TabType.AGENT, id: 'a-dragged', tileId: fromTile, position: 'n', workerId: 'w-1' })
      emitAddTab({ type: TabType.AGENT, id: 'b-head', tileId: toTile, position: 'n', workerId: 'w-1' })
      emitAddTab({ type: TabType.AGENT, id: 'c-next', tileId: toTile, position: 'nn', workerId: 'w-1' })

      const ops = useTileDragDrop({ view, selection, layoutStore, floatingWindowStore })
      // Drop onto the destination's HEAD: the dragged tab must take that
      // slot and displace `b-head` right.
      ops.handleCrossTileMove(
        fromTile,
        toTile,
        tabKey({ type: TabType.AGENT, id: 'a-dragged' }),
        tabKey({ type: TabType.AGENT, id: 'b-head' }),
      )

      const order = view.forTile(toTile).map(t => t.id)
      expect(order).toEqual(['a-dragged', 'b-head', 'c-next'])
      // No tie: a rank equal to a sibling's leaves the pair ordered by id,
      // which is arbitrary from the user's point of view.
      const positions = view.forTile(toTile).map(t => t.position)
      expect(new Set(positions).size).toBe(positions.length)
      dispose()
    })
  })
})
