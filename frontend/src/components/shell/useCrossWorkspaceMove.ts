import type { BatchOutcome } from './useOpsSubmitter'
import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { Tab } from '~/stores/tab.types'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { moveTabWorkspace, relocateFileTabPath } from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { positionAtInsertIdx } from '~/lib/lexorank'
import { emitMoveTabToWorkspace } from '~/stores/tabOps'
import { removeEmptyFloatingWindow } from './tileLifecycle'

/**
 * Point a tab's worker-side bookkeeping at `workspaceId`.
 *
 * AGENT / TERMINAL -> MoveTabWorkspace.
 * FILE             -> RelocateFileTabPath (E2EE; the hub never sees the path).
 *                     The worker emits FileTabPathRevoked on the source stream
 *                     and FileTabPathRegistered on the destination one so peers
 *                     update their caches.
 *
 * Both the forward move and the rejection rollback need this dispatch, and they
 * differ only in which workspace id they name — so the "FILE is the
 * path-carrying type" rule lived in two places that had to stay in step. A
 * third such type is now a one-line change here instead of a two-site edit.
 */
function relocateOrMoveWorkspace(workerId: string, tab: Tab, workspaceId: string): Promise<unknown> {
  return tab.type === TabType.FILE
    ? relocateFileTabPath(workerId, { tabId: tab.id, newWorkspaceId: workspaceId })
    : moveTabWorkspace(workerId, { tabType: tab.type, tabId: tab.id, newWorkspaceId: workspaceId })
}

export interface UseCrossWorkspaceMoveArgs {
  getActiveWorkspaceId: () => string | null
  view: TabView
  selection: TabSelectionStore
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore: FloatingWindowStoreType
  batchResultHandlers: Map<string, (outcome: BatchOutcome) => void>
  focusTile: (tileId: string, workspaceId?: string) => void
}

/**
 * Cross-workspace tab move.
 *
 * The CRDT half is a SINGLE `SetTabRegister(tile_id)` +
 * `SetTabRegister(position)` batch — never tombstone-then-re-add, which the
 * hub's remove-wins rule would silently drop. The worker RPC must succeed
 * BEFORE the CRDT batch goes out so the worker's local `workspace_id`
 * bookkeeping matches the CRDT-resolved workspace before any subscriber
 * observes the new state.
 *
 * This used to be twice as long. Every step branched on
 * `isSourceActive` / `isTargetActive`, because a tab lived in the active
 * `tabStore` or in a registry snapshot depending on which workspace was on
 * screen, and the move had to read from one, write to the other, and unwind
 * both on failure. With tabs joined from one projection there is no "which
 * store" question: the tab is wherever its `tile_id` resolves, the optimistic
 * update IS the op (pending ops are already in `speculativeState`), and
 * rollback is emitting the inverse.
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
    batchResultHandlers,
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

    const workerId = tab.workerId ?? ''
    const sourceTileId = tab.tileId

    const resolvedTargetTileId = resolveTargetTile(resolvedTargetWsId, targetTileId)
    if (!resolvedTargetTileId)
      return

    // 1) Worker RPC first, so the worker's workspace_id bookkeeping flips
    //    before any subscriber sees the CRDT move.
    //    AGENT / TERMINAL -> MoveTabWorkspace
    //    FILE             -> RelocateFileTabPath (E2EE; the hub never sees the
    //                        path). The worker emits FileTabPathRevoked on the
    //                        source stream and FileTabPathRegistered on the
    //                        destination one so peers update their caches.
    const rpcDone: Promise<unknown> = !workerId
      ? Promise.resolve()
      : relocateOrMoveWorkspace(workerId, tab, resolvedTargetWsId)

    rpcDone.then(() => {
      // 2) Emit the canonical move. This IS the optimistic update: the batch
      //    lands in speculativeState synchronously, so the projection — and
      //    therefore both workspaces' views — reflect it immediately.
      //
      //    The position is computed HERE, not before the await. A LexoRank is
      //    only unique relative to the tabs it was ranked against, and this
      //    handler is fire-and-forget with no in-flight guard — so two drops
      //    onto the same destination in quick succession (or one racing a
      //    peer's insert) would both rank against the same pre-RPC snapshot
      //    and mint byte-identical positions. The strip would then order those
      //    two tabs by tab id rather than by drop order.
      const tileTabs = view.forTile(resolvedTargetTileId)
      const resolvedTargetPosition = positionAtInsertIdx(tileTabs, tileTabs.length)
      const batchId = emitMoveTabToWorkspace(tab.type, tab.id, resolvedTargetTileId, resolvedTargetPosition)

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

      // If the hub rejects the batch (e.g. no write access to the destination),
      // reverse the worker-side update so the worker and the CRDT agree on
      // ownership. Transport timeouts do NOT trigger this: the submitter
      // retries with the same op_ids and principal-aware dedup returns the
      // original commit, so only an authoritative rejection gets here.
      if (batchId && workerId) {
        batchResultHandlers.set(batchId, (outcome) => {
          batchResultHandlers.delete(batchId)
          if (outcome.case !== 'rejected')
            return
          // The CRDT rejection already reverted placement; undo the worker
          // half. Its own error is swallowed — the submitter has already
          // warn-toasted the rejection to the user.
          relocateOrMoveWorkspace(workerId, tab, resolvedSourceWsId).catch(() => {})
        })
      }
    }).catch((err: unknown) => {
      // The worker RPC failed, so no CRDT op was emitted and the tab never
      // moved. There is nothing to unwind.
      showWarnToast('Failed to move tab', err)
    })
  }

  return { move }
}
