import type { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import { firstLeafId } from '~/stores/layout.store'

type LayoutStore = ReturnType<typeof createLayoutStore>
type FloatingWindowStore = ReturnType<typeof createFloatingWindowStore>
type TabStore = ReturnType<typeof createTabStore>

/**
 * Focus `tileId` on the correct owner. When the tile lives in a floating
 * window, mark it as that window's focused tile too — main-store focus alone
 * doesn't tell the window which inner tile holds the cursor.
 */
export function focusTile(
  layoutStore: LayoutStore,
  floatingWindowStore: FloatingWindowStore | undefined,
  tileId: string,
): void {
  const windowId = floatingWindowStore?.getWindowForTile(tileId) ?? null
  if (windowId)
    floatingWindowStore!.setFocusedTile(windowId, tileId)
  layoutStore.setFocusedTile(tileId)
}

/**
 * Move main-layout focus onto the first leaf in the main layout. Used after
 * a floating-window tile or its containing window is removed so focus
 * doesn't linger on a now-gone tile id. No-op if the layout is somehow
 * empty.
 */
export function refocusToFirstMainTile(layoutStore: LayoutStore): void {
  const id = firstLeafId(layoutStore.state.root)
  if (id)
    layoutStore.setFocusedTile(id)
}

/**
 * Post-drop cleanup after a floating window is disposed: migrate main-layout
 * focus back to the main tree if it was pointing at one of the disposed
 * tiles, then scrub tab-store entries for every disposed tile in one pass.
 * Shared by the per-tile dispose-empties-window path (`closeTile` returning
 * `{ kind: 'disposed' }`) and the user-driven close-window path
 * (`removeWindow`) so the focus invariant is encoded once.
 */
export function cleanupAfterWindowDisposal(
  layoutStore: LayoutStore,
  tabStore: TabStore,
  disposedTileIds: ReadonlySet<string> | string[],
): void {
  const idSet: ReadonlySet<string> = disposedTileIds instanceof Set
    ? disposedTileIds
    : new Set(disposedTileIds)
  const focusedId = layoutStore.focusedTileId()
  if (focusedId !== null && idSet.has(focusedId))
    refocusToFirstMainTile(layoutStore)
  tabStore.cleanupTiles(idSet)
}

/**
 * Drop the floating window that owns `tileId` if it has no remaining tabs.
 * Wraps the standard "resolve windowId → removeIfEmpty + refocus" sequence
 * so callers in tab-close, tab-attach, and cross-tile/-workspace drag
 * handlers don't reimplement it. No-op when the tile lives in the main
 * layout or `floatingWindowStore` is absent.
 */
export function removeEmptyFloatingWindow(
  layoutStore: LayoutStore,
  floatingWindowStore: FloatingWindowStore | undefined,
  tabStore: TabStore,
  tileId: string | undefined,
): boolean {
  if (!tileId || !floatingWindowStore)
    return false
  const windowId = floatingWindowStore.getWindowForTile(tileId)
  if (!windowId)
    return false
  return floatingWindowStore.removeIfEmpty(
    windowId,
    tId => tabStore.getTabsForTile(tId),
    (removedTileId) => {
      tabStore.cleanupTile(removedTileId)
      if (layoutStore.focusedTileId() === removedTileId)
        refocusToFirstMainTile(layoutStore)
    },
  )
}
