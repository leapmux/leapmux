import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { positionAtInsertIdx } from '~/lib/lexorank'
import { emitMoveTabToWorkspace } from '~/stores/tabOps'
import { removeEmptyFloatingWindow } from './tileLifecycle'

export interface UseCrossWorkspaceMoveArgs {
  getActiveWorkspaceId: () => string | null
  view: TabView
  selection: TabSelectionStore
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore: FloatingWindowStoreType
  focusTile: (tileId: string, workspaceId?: string) => void
}

/**
 * Cross-workspace tab move.
 *
 * The whole move is ONE CRDT batch: `SetTabRegister(tile_id)` +
 * `SetTabRegister(position)` — never tombstone-then-re-add, which the hub's
 * remove-wins rule would silently drop.
 *
 * NO WORKER RPC, which is what makes the move work with the worker offline.
 * There used to be one, and the CRDT batch had to wait for it so the worker's
 * local `workspace_id` bookkeeping matched the CRDT-resolved workspace before
 * any subscriber saw the new state — with a rollback on rejection, and a
 * "the tab never moved" toast when the worker was unreachable. The worker
 * stores no workspace id any more, so there is nothing to keep in step and
 * nothing to unwind: the CRDT is the only place a tab's workspace lives.
 *
 * This used to be twice as long for a second reason too. Every step branched on
 * `isSourceActive` / `isTargetActive`, because a tab lived in the active
 * `tabStore` or in a registry snapshot depending on which workspace was on
 * screen, and the move had to read from one and write to the other. With tabs
 * joined from one projection there is no "which store" question: the tab is
 * wherever its `tile_id` resolves, and the optimistic update IS the op (pending
 * ops are already in `speculativeState`).
 */
export function useCrossWorkspaceMove(args: UseCrossWorkspaceMoveArgs): {
  move: (targetWorkspaceId: string, draggedKey: string, sourceWorkspaceId?: string, targetTileId?: string) => void
} {
  const {
    getActiveWorkspaceId,
    view,
    selection,
    layoutStore,
    floatingWindowStore,
    focusTile,
  } = args

  /**
   * Leaf tile in `workspaceId` a moved tab should land on: the caller's choice,
   * else that workspace's focused tile, else its first leaf.
   *
   * The fallback matters because dragging onto a sidebar row is often the
   * user's first interaction with the destination, so there is no remembered
   * focus to use. It resolves through the projected TREE rather than reading
   * `rootNodeId` off the state: a workspace whose root has been split has a
   * SPLIT at that id, and a `tile_id` naming a non-leaf is a batch the hub
   * rejects — which reverts the optimistic move and drops the drag with no
   * visible error.
   *
   * The remembered focus comes through `focusedLeafIdFor`, which returns it
   * only while it is still a live leaf. The raw pointer has no such guarantee —
   * `useFocusInvariant` repairs only the ACTIVE workspace — so a destination the
   * user last visited before another client closed that tile would otherwise
   * hand the hub a dead id.
   */
  function resolveTargetTile(workspaceId: string, explicit: string | undefined): string {
    if (explicit)
      return explicit
    return layoutStore.focusedLeafIdFor(workspaceId) ?? layoutStore.firstLeafIdFor(workspaceId) ?? ''
  }

  const move = (targetWorkspaceId: string, draggedKey: string, sourceWorkspaceId?: string, targetTileId?: string): void => {
    const activeWsId = getActiveWorkspaceId()
    if (!activeWsId)
      return

    const tab = view.get(draggedKey)
    if (!tab)
      return

    // The tab knows its own workspace — resolved from tile_id by the
    // projection — so the caller's hint is only a fallback.
    const resolvedSourceWsId = tab.workspaceId || sourceWorkspaceId || activeWsId
    const resolvedTargetWsId = targetWorkspaceId === '__active__' ? activeWsId : targetWorkspaceId
    if (resolvedSourceWsId === resolvedTargetWsId)
      return

    const sourceTileId = tab.tileId

    const resolvedTargetTileId = resolveTargetTile(resolvedTargetWsId, targetTileId)
    if (!resolvedTargetTileId)
      return

    // Emit the canonical move. This IS the optimistic update: the batch lands
    // in speculativeState synchronously, so the projection — and therefore
    // both workspaces' views — reflect it immediately, whether or not the
    // tab's worker is reachable.
    const tileTabs = view.forTile(resolvedTargetTileId)
    const resolvedTargetPosition = positionAtInsertIdx(tileTabs, tileTabs.length)
    emitMoveTabToWorkspace(tab.type, tab.id, resolvedTargetTileId, resolvedTargetPosition)

    // Focus the destination tile in the DESTINATION workspace's slot. The
    // user may still be looking at the source workspace (dropping a tab onto
    // another workspace's sidebar row does not switch), and filing this under
    // the active workspace would point it at a tile outside its own tree —
    // which `useFocusInvariant` then resets to the first leaf, moving the
    // user's focus for them.
    focusTile(resolvedTargetTileId, resolvedTargetWsId)
    selection.setActiveById(tab.type, tab.id)
    // The source floating window may now be empty. Named with the SOURCE
    // workspace for the same reason `focusTile` above is named with the
    // destination: neither is necessarily the one on screen, and the focus
    // pointer this repairs belongs to the workspace the tile left.
    if (sourceTileId)
      removeEmptyFloatingWindow(layoutStore, floatingWindowStore, view, sourceTileId, resolvedSourceWsId)
  }

  return { move }
}
