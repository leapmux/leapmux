import type { BatchOutcome } from './useOpsSubmitter'
import type { PendingOpsManager } from '~/lib/crdt'
import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import type { Tab } from '~/stores/tab.types'
import type { createWorkspaceStoreRegistry } from '~/stores/workspaceStoreRegistry'
import { listTabsForWorkspace } from '~/api/listTabsBatcher'
import { moveTabWorkspace, relocateFileTabPath } from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { hlcIsZero } from '~/lib/crdt'
import { positionAtInsertIdx } from '~/lib/lexorank'
import { firstLeafId } from '~/stores/layout.store'
import { tabKey } from '~/stores/tab.helpers'
import { createEmptySnapshot } from '~/stores/workspaceStoreRegistry'
import { removeEmptyFloatingWindow } from './tileLifecycle'

export interface UseCrossWorkspaceMoveArgs {
  getActiveWorkspaceId: () => string | null
  getOrgId: () => string | undefined
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore: FloatingWindowStoreType
  registry: ReturnType<typeof createWorkspaceStoreRegistry>
  pendingMgr: () => PendingOpsManager | null
  batchResultHandlers: Map<string, (outcome: BatchOutcome) => void>
  focusTile: (tileId: string) => void
}

/**
 * Cross-workspace tab move handler. Per the plan's "Cross-workspace
 * move" contract the CRDT half is a SINGLE
 * `SetTabRegister(tile_id=newTileInW2)` +
 * `SetTabRegister(position=…)` batch (NOT a tombstone-then-re-add —
 * that would be silently dropped by the hub's remove-wins tombstone
 * rule). The worker RPC must succeed BEFORE the CRDT batch goes out
 * so the worker's local `workspace_id` bookkeeping matches the
 * CRDT-resolved workspace before any subscriber observes the new
 * state.
 *
 * Returns the move handler the sidebar / drag-drop callers invoke.
 */
export function useCrossWorkspaceMove(args: UseCrossWorkspaceMoveArgs): {
  move: (targetWorkspaceId: string, draggedKey: string, sourceWorkspaceId?: string, targetTileId?: string) => void
} {
  const {
    getActiveWorkspaceId,
    getOrgId,
    tabStore,
    layoutStore,
    floatingWindowStore,
    registry,
    pendingMgr,
    batchResultHandlers,
    focusTile,
  } = args

  const move = (targetWorkspaceId: string, draggedKey: string, sourceWorkspaceId?: string, targetTileId?: string): void => {
    const activeWsId = getActiveWorkspaceId()
    if (!activeWsId)
      return

    const resolvedSourceWsId = sourceWorkspaceId ?? activeWsId
    const resolvedTargetWsId = targetWorkspaceId === '__active__' ? activeWsId : targetWorkspaceId

    if (resolvedSourceWsId === resolvedTargetWsId)
      return

    const isSourceActive = resolvedSourceWsId === activeWsId
    const isTargetActive = resolvedTargetWsId === activeWsId

    let tab: Tab | undefined
    if (isSourceActive) {
      tab = tabStore.getTabByKey(draggedKey)
    }
    else {
      const sourceSnap = registry.get(resolvedSourceWsId)
      tab = sourceSnap?.tabs.find(t => tabKey(t) === draggedKey)
    }
    if (!tab)
      return

    let workerId = tab.workerId ?? ''
    if (!workerId && tab.type === TabType.AGENT) {
      workerId = tabStore.getAgentTab(tab.id)?.workerId ?? ''
    }

    // Resolve the destination tile and the LexoRank position the tab
    // will land at. Both are needed for the CRDT batch AND the
    // optimistic local view, so compute them up front.
    let resolvedTargetTileId: string
    let resolvedTargetPosition: string
    if (isTargetActive) {
      resolvedTargetTileId = targetTileId
        ?? (!isSourceActive ? (layoutStore.focusedTileId() ?? tab.tileId) : tab.tileId)
        ?? ''
      const tileTabs = resolvedTargetTileId
        ? tabStore.getTabsForTile(resolvedTargetTileId)
        : []
      resolvedTargetPosition = positionAtInsertIdx(tileTabs, tileTabs.length)
    }
    else {
      const targetSnap = registry.get(resolvedTargetWsId)
      // Fall back to the CRDT projection when the target workspace has
      // never been opened in this client session (no registry cache yet).
      // Dragging a tab onto a sidebar workspace item is the user's first
      // interaction with the destination; without this fallback we'd
      // submit a SetTabRegister(tile_id='') op that the hub rejects.
      let crdtTargetRoot = ''
      const mgr = pendingMgr()
      const specState = mgr?.state.speculativeState
      if (specState) {
        const ws = specState.workspaces[resolvedTargetWsId]
        if (ws?.rootNodeId) {
          // Verify the node exists and is a LEAF (the only valid
          // tile_id for a SetTabRegister(tile_id=…) op).
          const node = specState.nodes[ws.rootNodeId]
          if (node && hlcIsZero(node.tombstoneAt)) {
            crdtTargetRoot = ws.rootNodeId
          }
        }
      }
      resolvedTargetTileId = targetTileId
        ?? targetSnap?.layout.focusedTileId
        ?? (targetSnap ? firstLeafId(targetSnap.layout.root) : null)
        ?? crdtTargetRoot
      const tileTabs = (targetSnap?.tabs ?? []).filter(t => t.tileId === resolvedTargetTileId)
      resolvedTargetPosition = positionAtInsertIdx(tileTabs, tileTabs.length)
    }

    // Optimistic UI move. SILENT so the source-side removal doesn't
    // ship a TombstoneTab — the CRDT-side move is a single LWW write
    // to tile_id, NOT remove-then-recreate (Rule 4 of the plan's
    // "Apply transition rule" + the remove-wins clarification in
    // "Concurrency convergence").
    if (isSourceActive) {
      tabStore.removeTab(tab.type, tab.id, { silent: true })
    }
    else {
      registry.removeTab(resolvedSourceWsId, tab)
    }

    // If the source floating window is now empty, remove it.
    if (isSourceActive)
      removeEmptyFloatingWindow(layoutStore, floatingWindowStore, tabStore, tab.tileId)

    if (isTargetActive) {
      tabStore.addTab(
        { ...tab, tileId: resolvedTargetTileId, position: resolvedTargetPosition },
        { silent: true },
      )
      if (resolvedTargetTileId)
        focusTile(resolvedTargetTileId)
    }
    else {
      // Get or create a snapshot for the target workspace. Mark new
      // snapshots NOT tabsLoaded so the post-RPC ListTabs fetch will
      // fill in the hub's existing tabs before the user switches in.
      const targetSnap = registry.get(resolvedTargetWsId) ?? createEmptySnapshot(resolvedTargetWsId)
      // For AGENT tabs, the per-agent metadata travels on the Tab record
      // itself (see `protoToAgentTabFields`), so spreading `tab` carries
      // every field across the move.
      const newTab = { ...tab, tileId: resolvedTargetTileId, position: resolvedTargetPosition }
      const key = tabKey(newTab)
      registry.set(resolvedTargetWsId, {
        ...targetSnap,
        tabs: [...targetSnap.tabs, newTab],
        activeTabKey: key,
        tileActiveTabKeys: resolvedTargetTileId
          ? { ...(targetSnap.tileActiveTabKeys ?? {}), [resolvedTargetTileId]: key }
          : targetSnap.tileActiveTabKeys,
      })
    }

    // 1) Worker RPC first — the worker updates its `workspace_id`
    //    bookkeeping so subsequent listAgents / orphan-reconciler
    //    queries see the tab under the new workspace.
    //    - AGENT / TERMINAL → `MoveTabWorkspace`
    //    - FILE             → `RelocateFileTabPath` (E2EE; path stays
    //                          on the worker, hub never sees it). The
    //                          worker emits `FileTabPathRevoked` on the
    //                          source workspace stream and
    //                          `FileTabPathRegistered` on the
    //                          destination workspace stream, so peer
    //                          clients update their fileTabPaths cache.
    // 2) On worker success: emit the single CRDT batch.
    // 3) On worker failure: revert local UI optimism (no CRDT ops
    //    have shipped yet, so no rollback there).
    const currentOrgId = getOrgId()
    let rpcDone: Promise<unknown> = Promise.resolve()
    if (workerId) {
      if (tab.type === TabType.FILE) {
        if (currentOrgId) {
          rpcDone = relocateFileTabPath(workerId, {
            orgId: currentOrgId,
            tabId: tab.id,
            newWorkspaceId: resolvedTargetWsId,
          })
        }
      }
      else {
        rpcDone = moveTabWorkspace(workerId, {
          tabType: tab.type,
          tabId: tab.id,
          newWorkspaceId: resolvedTargetWsId,
        })
      }
    }

    rpcDone.then(async () => {
      // Worker bookkeeping has flipped. Submit the canonical move op
      // batch — one `SetTabRegister(tile_id)` + one
      // `SetTabRegister(position)`. The hub resolves the new owning
      // workspace via the new tile's ancestor chain; the
      // reconciliation effect absorbs the canonical state on echo.
      const batchId = tabStore.moveTabToWorkspace(tab!.type, tab!.id, resolvedTargetTileId, resolvedTargetPosition)
      // If the hub later rejects this batch (e.g. caller lacks write
      // access to the destination workspace), reverse the worker-side
      // workspace_id update we just made — the worker and the CRDT
      // must agree on which workspace owns the tab. Transport
      // timeouts do NOT trigger this rollback because the submitter
      // retries with the same op_ids; principal-aware dedup means the
      // original commit (if any) is returned, and the rollback only
      // fires on an authoritative rejection.
      if (batchId && workerId && currentOrgId) {
        batchResultHandlers.set(batchId, (outcome) => {
          batchResultHandlers.delete(batchId)
          if (outcome.case !== 'rejected')
            return
          // Reverse the worker-side change. We swallow the rollback
          // RPC's own error since by this point the user has already
          // seen the submitter's warn-toast for the rejection.
          if (tab!.type === TabType.FILE) {
            relocateFileTabPath(workerId, {
              orgId: currentOrgId,
              tabId: tab!.id,
              newWorkspaceId: resolvedSourceWsId,
            }).catch(() => {})
          }
          else {
            moveTabWorkspace(workerId, {
              tabType: tab!.type,
              tabId: tab!.id,
              newWorkspaceId: resolvedSourceWsId,
            }).catch(() => {})
          }
        })
      }

      // If the target snapshot was newly created (not fully loaded),
      // fetch the target workspace's existing tabs from the hub and
      // merge them into the cached snapshot so it reflects the full
      // tab list before the user switches into the target workspace.
      const targetSnap = registry.get(resolvedTargetWsId)
      if (currentOrgId && targetSnap && !targetSnap.tabsLoaded) {
        try {
          const resp = await listTabsForWorkspace(currentOrgId, resolvedTargetWsId)
          const existingKeys = new Set(targetSnap.tabs.map(t => tabKey(t)))
          const extraTabs: Tab[] = []
          for (const t of resp.tabs) {
            // Branch on the wire enum so the resulting object's `type`
            // is a literal matching one variant of the Tab union.
            const base = {
              id: t.tabId,
              position: t.position,
              tileId: t.tileId || targetSnap.layout.focusedTileId || '',
              workerId: t.workerId,
            }
            let fetched: Tab | null = null
            if (t.tabType === TabType.AGENT)
              fetched = { type: TabType.AGENT, ...base }
            else if (t.tabType === TabType.TERMINAL)
              fetched = { type: TabType.TERMINAL, ...base }
            else if (t.tabType === TabType.FILE)
              fetched = { type: TabType.FILE, ...base }
            if (fetched && !existingKeys.has(tabKey(fetched))) {
              extraTabs.push(fetched)
            }
          }
          registry.update(resolvedTargetWsId, snap => ({
            ...snap,
            tabs: [...snap.tabs, ...extraTabs],
            tabsLoaded: true,
          }))
        }
        catch { /* ignore — will be picked up on next restore */ }
      }
    }).catch((err: unknown) => {
      // Worker RPC failed — revert the optimistic UI update. No CRDT
      // op has been emitted yet so we just undo the local move.
      if (isTargetActive) {
        tabStore.removeTab(tab!.type, tab!.id, { silent: true })
      }
      else {
        registry.removeTab(resolvedTargetWsId, tab!)
      }

      if (isSourceActive) {
        tabStore.addTab(tab!, { silent: true })
      }
      else {
        // The Tab itself carries every per-agent field, so re-inserting
        // it on the source snapshot is enough — no separate agent
        // record to thread back.
        registry.update(resolvedSourceWsId, sourceSnap => ({
          ...sourceSnap,
          tabs: [...sourceSnap.tabs, tab!],
        }))
      }

      showWarnToast('Failed to move tab', err)
    })
  }

  return { move }
}
