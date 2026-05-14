import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import type { Tab } from '~/stores/tab.types'
import { firstLeafId } from '~/stores/layout.store'
import { focusTile as focusTileShared } from './tileLifecycle'
import { useTileMove } from './useTileMove'

/**
 * Wire up the four detach / attach / toggle / activate flows between
 * the main layout and floating-window stores. Each one mutates the
 * stores in lock-step (move the tab, focus the destination tile,
 * clean up the source side) and benefits from a single colocated
 * implementation so the order of operations is obvious.
 *
 * The hook is a thin compositional layer — there's no state of its
 * own. Returning an object lets `AppShell.tsx` thread the handlers
 * into its TabContext and `useTabOperations` adapter without
 * inlining the full bodies.
 */
export interface UseFloatingWindowOpsArgs {
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore: FloatingWindowStoreType
  tabStore: ReturnType<typeof createTabStore>
}

export interface FloatingWindowOps {
  handleDetachTab: (tab: Tab) => void
  handleAttachTab: (tab: Tab) => void
  handleToggleFloatingTab: () => void
  handleActivateFloatingWindow: (windowId: string) => void
}

export function useFloatingWindowOps(args: UseFloatingWindowOpsArgs): FloatingWindowOps {
  const { layoutStore, floatingWindowStore, tabStore } = args
  const tileMove = useTileMove({ tabStore, layoutStore, floatingWindowStore })

  const focusTile = (tileId: string): void => {
    focusTileShared(layoutStore, floatingWindowStore, tileId)
  }

  const handleDetachTab = (tab: Tab): void => {
    const sourceTileId = tab.tileId
    const created = floatingWindowStore.addWindow()
    if (!created)
      return
    // cleanupSource=false here because detach's source is in the MAIN
    // tree, not a floating window — `removeEmptyFloatingWindow` would
    // no-op, and the empty-main-tile sweep below is detach-specific.
    tileMove.moveTabToTile(tab, created.tileId, { takeFocus: true, cleanupSource: false })
    // Close the source tile if it's now empty and the main layout has
    // multiple tiles — a popped-out tab leaves a hole otherwise.
    if (sourceTileId
      && tabStore.getTabsForTile(sourceTileId).length === 0
      && layoutStore.hasMultipleTiles()) {
      layoutStore.closeTile(sourceTileId)
      tabStore.cleanupTile(sourceTileId)
    }
  }

  const handleAttachTab = (tab: Tab): void => {
    const sourceTileId = tab.tileId
    if (!sourceTileId || !floatingWindowStore.getWindowForTile(sourceTileId))
      return

    const targetTileId = firstLeafId(layoutStore.state.root)
    if (!targetTileId)
      return

    tileMove.moveTabToTile(tab, targetTileId, { takeFocus: true, cleanupSource: true })
  }

  const handleToggleFloatingTab = (): void => {
    const tileId = layoutStore.focusedTileId()
    if (!tileId)
      return
    const tab = tabStore.getActiveTabForTile(tileId)
    if (!tab)
      return
    if (floatingWindowStore.getWindowForTile(tileId))
      handleAttachTab(tab)
    else
      handleDetachTab(tab)
  }

  const handleActivateFloatingWindow = (windowId: string): void => {
    const tileId = floatingWindowStore.getWindow(windowId)?.focusedTileId
    if (tileId)
      focusTile(tileId)
  }

  return {
    handleDetachTab,
    handleAttachTab,
    handleToggleFloatingTab,
    handleActivateFloatingWindow,
  }
}
