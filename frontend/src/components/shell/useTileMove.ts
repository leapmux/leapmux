import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { Tab } from '~/stores/tab.types'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { emitMoveTabToTile } from '~/stores/tabOps'
import { focusTile, removeEmptyFloatingWindow } from './tileLifecycle'

/**
 * useTileMove encapsulates the "move tab → activate on destination →
 * (maybe) follow focus → (maybe) sweep an empty floating source"
 * sequence shared by every cross-tile / detach / attach flow in the
 * shell. Hoisting the body out of `useFloatingWindowOps` and
 * `useTileDragDrop` keeps the lock-step ordering in one place and
 * removes the most-duplicated branch in those hooks.
 *
 * Source cleanup is a flag rather than a callback because every
 * existing caller wanted exactly the same shape — drop the floating
 * window that owned the source tile if it's now empty. Callers with
 * additional source cleanup (e.g. close an empty MAIN-tree tile when
 * detaching) opt out of the standard sweep and run their own.
 *
 * Focus-follow is also a flag: drag handlers compute it from
 * "was this tab active on the source?" (so focus migrates with the
 * dragged tab), while attach/detach handlers always pass `true`
 * because the user explicitly asked to move attention.
 */
export interface UseTileMoveArgs {
  view: TabView
  selection: TabSelectionStore
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore: FloatingWindowStoreType
}

export interface MoveTabToTileOptions {
  /**
   * When true, layout focus (and the destination floating window's
   * inner focus, if applicable) follows the moved tab. Used by drag
   * handlers when the dragged tab was the active one on its source —
   * the user is carrying their attention with them — and always set
   * by detach/attach handlers (those are explicit focus moves).
   */
  takeFocus: boolean
  /**
   * When true, the source floating window is removed if the move
   * emptied its last tile. Pass false when the caller has its own
   * specialized source cleanup (e.g. detach closes the empty MAIN
   * tile, not a floating window).
   */
  cleanupSource: boolean
}

export interface UseTileMoveOps {
  moveTabToTile: (tab: Tab, destTileId: string, options: MoveTabToTileOptions) => void
}

export function useTileMove(args: UseTileMoveArgs): UseTileMoveOps {
  const { view, selection, layoutStore, floatingWindowStore } = args

  function moveTabToTile(tab: Tab, destTileId: string, options: MoveTabToTileOptions): void {
    const sourceTileId = tab.tileId
    // Emitting the op IS the move: the batch lands in speculativeState
    // synchronously, so the projection reflects the new tile before this
    // returns and there is no local placement to update alongside it.
    emitMoveTabToTile(tab.type, tab.id, destTileId)
    // The destination tile always fronts the moved tab. Whether it ALSO becomes
    // the workspace's active tab is exactly the `takeFocus` question: a drop
    // that did not take focus came from a tab the user was not looking at
    // (`wasActiveOnSource === false` in `useTileDragDrop`), and claiming the
    // workspace pointer for it would badge the tab they ARE reading and seed
    // the next new agent from the wrong provider.
    const moved = view.getById(tab.type, tab.id) ?? tab
    if (options.takeFocus) {
      selection.setActive(moved)
      focusTile(layoutStore, floatingWindowStore, destTileId)
    }
    else {
      selection.setActiveInTile(moved, destTileId)
    }
    if (options.cleanupSource && sourceTileId)
      removeEmptyFloatingWindow(layoutStore, floatingWindowStore, view, sourceTileId)
  }

  return { moveTabToTile }
}
