import type { AgentTab } from '~/stores/tab.types'
import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { useTileMove } from '~/components/shell/useTileMove'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { setCRDTBridge } from '~/lib/crdt'
import { tabKey } from '~/stores/tab.helpers'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestFloatingWindowStore, createTestTabStores } from '~/test-support/tabStores'

afterEach(() => setCRDTBridge(null))

/**
 * useTileMove is the shared move+activate+follow+cleanup helper that
 * the detach/attach/cross-tile-drag flows all rely on. Each option
 * combination has a distinct user-visible outcome — focus carrying
 * with the dragged tab, leaving an empty floating window vs. keeping
 * it, and the no-op behaviour when the source tile is in the main
 * tree. These tests pin each axis so a future tweak to one flag
 * doesn't silently regress another caller.
 */
describe('useTileMove.moveTabToTile', () => {
  function setup() {
    installTestBridge({ rootTileId: 'root-leaf' })
    const stores = createTestTabStores('ws-test')
    const floatingWindowStore = createTestFloatingWindowStore()
    const ops = useTileMove({ ...stores, floatingWindowStore })
    return { ...stores, floatingWindowStore, ops }
  }

  /** Place a tab through the op path and read it back off the projection. */
  function place(view: ReturnType<typeof setup>['view'], id: string, tileId: string, position: string): AgentTab {
    emitAddTab({ type: TabType.AGENT, id, tileId, position, workerId: 'w-1' })
    return view.getAgentTab(id)!
  }

  it('happy path: takeFocus=true + cleanupSource=true moves tab, follows focus, removes empty floating source', () => {
    createRoot((dispose) => {
      const { view, selection, layoutStore, floatingWindowStore, ops } = setup()
      const win = floatingWindowStore.addWindow()!
      const { windowId, tileId: floatingTile } = win
      const mainTile = layoutStore.getAllTileIds()[0]
      // One tab on the floating window's root tile — moving it out
      // empties the window.
      const tab = place(view, 'agent-1', floatingTile, 'a')
      selection.setActiveById(TabType.AGENT, 'agent-1')
      layoutStore.setFocusedTile(floatingTile)

      ops.moveTabToTile(tab, mainTile, { takeFocus: true, cleanupSource: true })

      // Tab moved.
      expect(view.get(tabKey(tab))?.tileId).toBe(mainTile)
      // Destination tile activated for the moved tab.
      expect(selection.activeKeyForTile(mainTile)).toBe(tabKey(tab))
      // Focus followed.
      expect(layoutStore.focusedTileId()).toBe(mainTile)
      // Empty source floating window was disposed.
      expect(floatingWindowStore.getWindow(windowId)).toBeNull()
      dispose()
    })
  })

  it('takeFocus=false leaves focus on the original tile', () => {
    createRoot((dispose) => {
      const { view, selection, layoutStore, ops } = setup()
      const toTile = layoutStore.splitTile('root-leaf', 'horizontal')!
      const [tileA, tileB] = layoutStore.getAllTileIds()
      const fromTile = tileB === toTile ? tileA : tileB
      const tab = place(view, 'a-bg', fromTile, 'a')
      place(view, 'a-active', fromTile, 'b')
      selection.setActiveById(TabType.AGENT, 'a-active')
      layoutStore.setFocusedTile(fromTile)

      ops.moveTabToTile(tab, toTile, { takeFocus: false, cleanupSource: true })

      expect(view.get(tabKey(tab))?.tileId).toBe(toTile)
      expect(selection.activeKeyForTile(toTile)).toBe(tabKey(tab))
      // Focus stayed on source — the bg tab move shouldn't steal it.
      expect(layoutStore.focusedTileId()).toBe(fromTile)
      dispose()
    })
  })

  /**
   * The tile pointer and the WORKSPACE pointer answer different questions, and
   * a focus-less move may only move the first. The workspace pointer feeds the
   * notification-badge suppression in `useWorkspaceConnection`, `activeFilePath`
   * in `AppShell`, and `resolvePreferredProvider`'s seed for a new agent — so a
   * background drag that claimed it would badge the tab the user is reading and
   * seed the next agent from the dragged one.
   */
  it('takeFocus=false does not reassign the workspace active tab', () => {
    createRoot((dispose) => {
      const { view, selection, layoutStore, ops } = setup()
      const toTile = layoutStore.splitTile('root-leaf', 'horizontal')!
      const [tileA, tileB] = layoutStore.getAllTileIds()
      const fromTile = tileB === toTile ? tileA : tileB
      const dragged = place(view, 'a-bg', fromTile, 'a')
      const reading = place(view, 'a-active', fromTile, 'b')
      selection.setActiveById(TabType.AGENT, 'a-active')
      expect(selection.activeKeyForWorkspace('ws-test')).toBe(tabKey(reading))

      ops.moveTabToTile(dragged, toTile, { takeFocus: false, cleanupSource: true })

      expect(selection.activeKeyForTile(toTile), 'destination tile fronts it').toBe(tabKey(dragged))
      expect(
        selection.activeKeyForWorkspace('ws-test'),
        'the tab the user is reading keeps the workspace pointer',
      ).toBe(tabKey(reading))
      dispose()
    })
  })

  it('takeFocus=true does reassign the workspace active tab', () => {
    createRoot((dispose) => {
      const { view, selection, layoutStore, ops } = setup()
      const toTile = layoutStore.splitTile('root-leaf', 'horizontal')!
      const [tileA, tileB] = layoutStore.getAllTileIds()
      const fromTile = tileB === toTile ? tileA : tileB
      const dragged = place(view, 'a-bg', fromTile, 'a')
      place(view, 'a-active', fromTile, 'b')
      selection.setActiveById(TabType.AGENT, 'a-active')

      ops.moveTabToTile(dragged, toTile, { takeFocus: true, cleanupSource: true })

      expect(selection.activeKeyForWorkspace('ws-test')).toBe(tabKey(dragged))
      dispose()
    })
  })

  it('cleanupSource=false keeps the source floating window alive even when emptied', () => {
    createRoot((dispose) => {
      const { view, layoutStore, floatingWindowStore, ops } = setup()
      const win = floatingWindowStore.addWindow()!
      const { windowId, tileId: floatingTile } = win
      const mainTile = layoutStore.getAllTileIds()[0]
      const tab = place(view, 'agent-1', floatingTile, 'a')

      // detach passes cleanupSource=false because its source is in
      // the MAIN tree; this test pins the FALSE branch directly so
      // a future refactor that always sweeps can't silently break
      // detach.
      ops.moveTabToTile(tab, mainTile, { takeFocus: true, cleanupSource: false })

      // Tab still moved, but the now-empty floating window stays.
      expect(view.get(tabKey(tab))?.tileId).toBe(mainTile)
      expect(floatingWindowStore.getWindow(windowId)).not.toBeNull()
      dispose()
    })
  })

  it('cleanupSource=true is a safe no-op when the source tile lives in the main tree', () => {
    createRoot((dispose) => {
      const { view, layoutStore, ops } = setup()
      const toTile = layoutStore.splitTile('root-leaf', 'horizontal')!
      const [tileA, tileB] = layoutStore.getAllTileIds()
      const fromTile = tileB === toTile ? tileA : tileB
      const tab = place(view, 'a-1', fromTile, 'a')

      // removeEmptyFloatingWindow short-circuits for main-tree
      // sources (floatingWindowStore.getWindowForTile returns null),
      // so this must complete without throwing or mutating the main
      // tree.
      const beforeMainTiles = layoutStore.getAllTileIds().slice().sort()
      ops.moveTabToTile(tab, toTile, { takeFocus: false, cleanupSource: true })

      expect(view.get(tabKey(tab))?.tileId).toBe(toTile)
      // Main layout structure preserved — fromTile still exists (it
      // just has no tabs anymore).
      expect(layoutStore.getAllTileIds().slice().sort()).toEqual(beforeMainTiles)
      dispose()
    })
  })

  // `Tab.tileId` is optional in the base type even though every tab the
  // projection assembles has one, so a caller can still hand this helper an
  // unplaced tab literal. The `&& sourceTileId` guard is what stops the
  // cleanup sweep from being handed `undefined`.
  it('does not crash when the tab has no source tileId', () => {
    createRoot((dispose) => {
      const { view, selection, layoutStore, ops } = setup()
      const mainTile = layoutStore.getAllTileIds()[0]
      const tab: AgentTab = { type: TabType.AGENT, id: 'orphan', workspaceId: 'ws-1', tileId: undefined, workerId: 'w-1' }

      ops.moveTabToTile(tab, mainTile, { takeFocus: true, cleanupSource: true })

      // The move op still landed — emitting it is what places the tab, so
      // it arrives in the projection on the destination tile.
      expect(view.get(tabKey(tab))?.tileId).toBe(mainTile)
      expect(selection.activeKeyForTile(mainTile)).toBe(tabKey(tab))
      dispose()
    })
  })
})
