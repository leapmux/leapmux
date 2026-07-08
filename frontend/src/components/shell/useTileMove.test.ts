import type { AgentTab } from '~/stores/tab.types'
import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { useTileMove } from '~/components/shell/useTileMove'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { setCRDTBridge } from '~/lib/crdt'
import { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import { createLayoutStore } from '~/stores/layout.store'
import { tabKey } from '~/stores/tab.helpers'
import { createTabStore } from '~/stores/tab.store'
import { installTestBridge } from '~/test-support/crdtBridge'

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
    const tabStore = createTabStore()
    const layoutStore = createLayoutStore()
    const floatingWindowStore = createFloatingWindowStore()
    return { tabStore, layoutStore, floatingWindowStore }
  }

  it('happy path: takeFocus=true + cleanupSource=true moves tab, follows focus, removes empty floating source', () => {
    createRoot((dispose) => {
      const { tabStore, layoutStore, floatingWindowStore } = setup()
      const win = floatingWindowStore.addWindow()!
      const { windowId, tileId: floatingTile } = win
      const mainTile = layoutStore.getAllTileIds()[0]
      // One tab on the floating window's root tile — moving it out
      // empties the window.
      const tab: AgentTab = { type: TabType.AGENT, id: 'agent-1', tileId: floatingTile, workerId: 'w-1' }
      tabStore.addTab(tab)
      tabStore.setActiveTabForTile(floatingTile, TabType.AGENT, 'agent-1')
      layoutStore.setFocusedTile(floatingTile)

      const ops = useTileMove({ tabStore, layoutStore, floatingWindowStore })
      ops.moveTabToTile(tab, mainTile, { takeFocus: true, cleanupSource: true })

      // Tab moved.
      expect(tabStore.getTabByKey(tabKey(tab))?.tileId).toBe(mainTile)
      // Destination tile activated for the moved tab.
      expect(tabStore.getActiveTabKeyForTile(mainTile)).toBe(tabKey(tab))
      // Focus followed.
      expect(layoutStore.focusedTileId()).toBe(mainTile)
      // Empty source floating window was disposed.
      expect(floatingWindowStore.getWindow(windowId)).toBeNull()
      dispose()
    })
  })

  it('takeFocus=false leaves focus on the original tile', () => {
    createRoot((dispose) => {
      const { tabStore, layoutStore, floatingWindowStore } = setup()
      const otherTileId = layoutStore.splitTile('root-leaf', 'horizontal')!
      const [tileA, tileB] = layoutStore.getAllTileIds()
      const fromTile = tileB === otherTileId ? tileA : tileB
      const toTile = otherTileId
      const tab: AgentTab = { type: TabType.AGENT, id: 'a-bg', tileId: fromTile, workerId: 'w-1' }
      tabStore.addTab(tab, { activate: false })
      tabStore.addTab({ type: TabType.AGENT, id: 'a-active', tileId: fromTile, workerId: 'w-1' })
      tabStore.setActiveTabForTile(fromTile, TabType.AGENT, 'a-active')
      layoutStore.setFocusedTile(fromTile)

      const ops = useTileMove({ tabStore, layoutStore, floatingWindowStore })
      ops.moveTabToTile(tab, toTile, { takeFocus: false, cleanupSource: true })

      expect(tabStore.getTabByKey(tabKey(tab))?.tileId).toBe(toTile)
      expect(tabStore.getActiveTabKeyForTile(toTile)).toBe(tabKey(tab))
      // Focus stayed on source — the bg tab move shouldn't steal it.
      expect(layoutStore.focusedTileId()).toBe(fromTile)
      dispose()
    })
  })

  it('cleanupSource=false keeps the source floating window alive even when emptied', () => {
    createRoot((dispose) => {
      const { tabStore, layoutStore, floatingWindowStore } = setup()
      const win = floatingWindowStore.addWindow()!
      const { windowId, tileId: floatingTile } = win
      const mainTile = layoutStore.getAllTileIds()[0]
      const tab: AgentTab = { type: TabType.AGENT, id: 'agent-1', tileId: floatingTile, workerId: 'w-1' }
      tabStore.addTab(tab)

      const ops = useTileMove({ tabStore, layoutStore, floatingWindowStore })
      // detach passes cleanupSource=false because its source is in
      // the MAIN tree; this test pins the FALSE branch directly so
      // a future refactor that always sweeps can't silently break
      // detach.
      ops.moveTabToTile(tab, mainTile, { takeFocus: true, cleanupSource: false })

      // Tab still moved, but the now-empty floating window stays.
      expect(tabStore.getTabByKey(tabKey(tab))?.tileId).toBe(mainTile)
      expect(floatingWindowStore.getWindow(windowId)).not.toBeNull()
      dispose()
    })
  })

  it('cleanupSource=true is a safe no-op when the source tile lives in the main tree', () => {
    createRoot((dispose) => {
      const { tabStore, layoutStore, floatingWindowStore } = setup()
      const otherTileId = layoutStore.splitTile('root-leaf', 'horizontal')!
      const [tileA, tileB] = layoutStore.getAllTileIds()
      const fromTile = tileB === otherTileId ? tileA : tileB
      const toTile = otherTileId
      const tab: AgentTab = { type: TabType.AGENT, id: 'a-1', tileId: fromTile, workerId: 'w-1' }
      tabStore.addTab(tab)

      const ops = useTileMove({ tabStore, layoutStore, floatingWindowStore })
      // removeEmptyFloatingWindow short-circuits for main-tree
      // sources (floatingWindowStore.getWindowForTile returns null),
      // so this must complete without throwing or mutating the main
      // tree.
      const beforeMainTiles = layoutStore.getAllTileIds().slice().sort()
      ops.moveTabToTile(tab, toTile, { takeFocus: false, cleanupSource: true })

      expect(tabStore.getTabByKey(tabKey(tab))?.tileId).toBe(toTile)
      // Main layout structure preserved — fromTile still exists (it
      // just has no tabs anymore).
      const afterMainTiles = layoutStore.getAllTileIds().slice().sort()
      expect(afterMainTiles).toEqual(beforeMainTiles)
      dispose()
    })
  })

  it('does not crash when the tab has no source tileId (mid-restore case)', () => {
    createRoot((dispose) => {
      const { tabStore, layoutStore, floatingWindowStore } = setup()
      const mainTile = layoutStore.getAllTileIds()[0]
      // Synthesize a tab object with `tileId: undefined`. The store
      // is otherwise idle; we're testing the helper's handling of the
      // unplaced-tab edge case (mid-restore / pre-bridge).
      const tab: AgentTab = { type: TabType.AGENT, id: 'orphan', tileId: undefined, workerId: 'w-1' }
      tabStore.addTab(tab, { silent: true })

      const ops = useTileMove({ tabStore, layoutStore, floatingWindowStore })
      ops.moveTabToTile(tab, mainTile, { takeFocus: true, cleanupSource: true })

      // Move landed on the destination; source cleanup was skipped
      // (no sourceTileId to sweep).
      expect(tabStore.getTabByKey(tabKey(tab))?.tileId).toBe(mainTile)
      expect(tabStore.getActiveTabKeyForTile(mainTile)).toBe(tabKey(tab))
      dispose()
    })
  })
})
