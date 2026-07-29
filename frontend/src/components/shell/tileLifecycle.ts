import type { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { TabView } from '~/stores/tabView'
import { firstLeafId } from '~/stores/layout.store'

type LayoutStore = ReturnType<typeof createLayoutStore>
type FloatingWindowStore = ReturnType<typeof createFloatingWindowStore>

/**
 * Focus `tileId` on the correct owner. When the tile lives in a floating
 * window, mark it as that window's focused tile too — main-store focus alone
 * doesn't tell the window which inner tile holds the cursor.
 *
 * `workspaceId` names which workspace's focus slot the tile belongs to and
 * defaults to the active one. A cross-workspace move must pass it: the tile is
 * in the DESTINATION workspace, and recording it under the workspace still on
 * screen leaves that workspace focused on a tile outside its own tree.
 */
export function focusTile(
  layoutStore: LayoutStore,
  floatingWindowStore: FloatingWindowStore | undefined,
  tileId: string,
  workspaceId?: string,
): void {
  const windowId = floatingWindowStore?.getWindowForTile(tileId) ?? null
  if (windowId)
    floatingWindowStore!.setFocusedTile(windowId, tileId)
  layoutStore.setFocusedTile(tileId, workspaceId)
}

/**
 * Move main-layout focus onto the first leaf of `workspaceId`'s main layout.
 * Used after a floating-window tile or its containing window is removed so
 * focus doesn't linger on a now-gone tile id. No-op if the layout is somehow
 * empty.
 *
 * `workspaceId` defaults to the active one, and a caller that can be reached
 * for a background workspace MUST pass it — for the same reason `focusTile`
 * takes it. Repairing the active workspace's pointer when the tile that
 * vanished belonged to another one leaves the real dangling pointer in place
 * and moves a pointer that was fine.
 */
export function refocusToFirstMainTile(layoutStore: LayoutStore, workspaceId?: string): void {
  const id = workspaceId === undefined
    ? firstLeafId(layoutStore.state.root)
    : layoutStore.firstLeafIdFor(workspaceId)
  if (id)
    layoutStore.setFocusedTile(id, workspaceId)
}

/**
 * Post-drop cleanup after a floating window is disposed: migrate main-layout
 * focus back to the main tree if it was pointing at one of the disposed tiles.
 * Shared by the per-tile dispose-empties-window path (`closeTile` returning
 * `{ kind: 'disposed' }`) and the user-driven close-window path
 * (`removeWindow`) so the focus invariant is encoded once.
 *
 * Per-tile SELECTION entries are not scrubbed here — `useSelectionSweep` owns
 * that, gated on the projection, and reclaims them when the window leaves it.
 */
export function cleanupAfterWindowDisposal(
  layoutStore: LayoutStore,
  disposedTileIds: ReadonlySet<string> | string[],
): void {
  const idSet: ReadonlySet<string> = disposedTileIds instanceof Set
    ? disposedTileIds
    : new Set(disposedTileIds)
  const focusedId = layoutStore.focusedTileId()
  if (focusedId !== null && idSet.has(focusedId))
    refocusToFirstMainTile(layoutStore)
}

/**
 * Drop the floating window that owns `tileId` if it has no remaining tabs.
 * Wraps the standard "resolve windowId → removeIfEmpty + refocus" sequence
 * so callers in tab-close, tab-attach, and cross-tile/-workspace drag
 * handlers don't reimplement it. No-op when the tile lives in the main
 * layout or `floatingWindowStore` is absent.
 *
 * `workspaceId` names the workspace the tile belongs to, and the
 * cross-workspace drag path passes the SOURCE workspace — which is not the one
 * on screen. Both halves need it: the window itself is resolved account-wide,
 * and so is the focus pointer repaired afterwards. Comparing the ACTIVE
 * workspace's focus against a removed tile in another one never matches, so
 * that workspace kept a pointer at a disposed floating tile until the user
 * switched back into it.
 */
export function removeEmptyFloatingWindow(
  layoutStore: LayoutStore,
  floatingWindowStore: FloatingWindowStore | undefined,
  view: TabView,
  tileId: string | undefined,
  workspaceId?: string,
): boolean {
  if (!tileId || !floatingWindowStore)
    return false
  const windowId = floatingWindowStore.getWindowForTile(tileId)
  if (!windowId)
    return false
  return floatingWindowStore.removeIfEmpty(
    windowId,
    tId => view.forTile(tId),
    (removedTileId) => {
      const focused = workspaceId === undefined
        ? layoutStore.focusedTileId()
        : layoutStore.focusedTileIdFor(workspaceId)
      if (focused === removedTileId)
        refocusToFirstMainTile(layoutStore, workspaceId)
    },
  )
}
