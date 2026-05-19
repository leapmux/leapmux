import type { TabContext } from './tabContext'
import type { useAgentOperations } from './useAgentOperations'
import type { useTerminalOperations } from './useTerminalOperations'
import type { LastTabCloseChoice, LastTabConfirmState } from '~/components/shell/LastTabCloseDialog'
import type { InspectLastTabCloseResponse } from '~/generated/leapmux/v1/git_pb'
import type { createChatStore } from '~/stores/chat.store'
import type { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import type { FileOpenSource, FileTab, Tab } from '~/stores/tab.types'
import type { WorkspaceStoreRegistryType } from '~/stores/workspaceStoreRegistry'
import { batch, createEffect, createSignal } from 'solid-js'
import { isWorkerUnreachable } from '~/api/workerErrors'
import * as workerRpc from '~/api/workerRpc'
import { showInfoToast, showWarnToast } from '~/components/common/Toast'
import { toastCloseFailure } from '~/components/shell/closeFailureToast'
import { getTerminalInstance } from '~/components/terminal/TerminalView'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createDialogState } from '~/hooks/createDialogState'
import { makeIdGenerator } from '~/lib/idGenerator'
import { basename } from '~/lib/paths'
import { MAX_BACKGROUND_CHAT_MESSAGES } from '~/stores/chat.store'
import { tabKey } from '~/stores/tab.helpers'
import { removeEmptyFloatingWindow } from './tileLifecycle'

interface UseTabOperationsOpts {
  tabStore: ReturnType<typeof createTabStore>
  chatStore: ReturnType<typeof createChatStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore?: ReturnType<typeof createFloatingWindowStore>
  agentOps: ReturnType<typeof useAgentOperations>
  termOps: ReturnType<typeof useTerminalOperations>
  activeTab: () => Tab | undefined
  getCurrentTabContext: () => TabContext
  focusEditor: () => void
  getScrollState: () => { distFromBottom: number, atBottom: boolean } | undefined
  setFileTreePath: (path: string) => void
  /** Org id used for file-tab E2EE worker RPCs. */
  getOrgId: () => string | undefined
  /** Active workspace id used for file-tab E2EE worker RPCs. */
  getActiveWorkspaceId: () => string | undefined
  /**
   * Per-workspace registry. Used by `handleTabClose` to detect that a
   * sidebar-driven close targets a tab in a non-active workspace and
   * to remove the row from that workspace's cached snapshot. The
   * active-workspace tabStore only knows about the currently-rendered
   * workspace's tabs, so a cross-workspace close that goes through it
   * is a silent no-op locally.
   */
  registry: WorkspaceStoreRegistryType
}

export function useTabOperations(opts: UseTabOperationsOpts) {
  const {
    tabStore,
    chatStore,
    layoutStore,
    floatingWindowStore,
    agentOps,
    termOps,
    activeTab,
    getCurrentTabContext,
    focusEditor,
    getScrollState,
    setFileTreePath,
    getOrgId,
    getActiveWorkspaceId,
    registry,
  } = opts

  const [closingTabKeys, setClosingTabKeys] = createSignal<Set<string>>(new Set())

  const lastTabConfirmDialog = createDialogState<LastTabConfirmState>()

  let isTabEditing: () => boolean = () => false

  const addClosingTabKey = (key: string) =>
    setClosingTabKeys(prev => new Set([...prev, key]))
  const removeClosingTabKey = (key: string) =>
    setClosingTabKeys((prev) => {
      const next = new Set(prev)
      next.delete(key)
      return next
    })

  const handleTabSelect = (tab: Tab) => {
    // Read scroll state before any store updates so the DOM measurement
    // happens while the previous tab is still visible. "Active agent"
    // is now derived: if the previously-active tab was an AGENT, use
    // its id.
    const prevTab = activeTab()
    const prevAgentId = prevTab?.type === TabType.AGENT ? prevTab.id : null
    const scrollState = prevAgentId ? getScrollState() : undefined

    // Batch the scroll-save and tab-switch store updates so that
    // SolidJS defers effects until both are applied.  Without this,
    // the savedViewportScroll effect fires while the old tab is still
    // visible, schedules a rAF that clears the saved state, and by the
    // time the user switches back the saved state is gone.
    batch(() => {
      if (prevAgentId && scrollState !== undefined) {
        chatStore.saveViewportScroll(prevAgentId, scrollState.distFromBottom, scrollState.atBottom)
      }
      tabStore.activateTab(tab.tileId ?? '', tab.type, tab.id)
    })

    // When switching tabs within the same tile, the previous agent becomes
    // hidden immediately. Trim it now instead of waiting for future messages
    // or for the visible ChatView's bottom-sticky path to run.
    if (
      prevAgentId
      && prevTab?.type === TabType.AGENT
      && prevTab.id !== tab.id
      && prevTab.tileId
      && prevTab.tileId === tab.tileId
      && chatStore.getMessages(prevAgentId).length > MAX_BACKGROUND_CHAT_MESSAGES
    ) {
      chatStore.trimOldMessages(prevAgentId, MAX_BACKGROUND_CHAT_MESSAGES)
    }

    if (tab.type === TabType.AGENT) {
      requestAnimationFrame(() => {
        if (isTabEditing())
          return
        focusEditor()
      })
    }
    else if (tab.type === TabType.TERMINAL) {
      requestAnimationFrame(() => {
        if (isTabEditing())
          return
        const instance = getTerminalInstance(tab.id)
        instance?.terminal.focus()
      })
    }
  }

  const askLastTabConfirmation = (workerId: string, tabType: TabType, tabId: string, status: InspectLastTabCloseResponse): Promise<LastTabCloseChoice> => {
    return new Promise((resolve) => {
      lastTabConfirmDialog.open({ ...status, workerId, tabId, tabType, resolve })
    })
  }

  const removeEmptyFloatingWindowForTile = (tileId: string | undefined) =>
    removeEmptyFloatingWindow(layoutStore, floatingWindowStore, tabStore, tileId)

  // After a tab close empties the focused tile, follow the surviving
  // active tab to its tile. removeTab already MRU-promoted the next
  // tab globally; leaving focus on the now-empty tile would leave
  // the user looking at an EmptyTilePlaceholder while the work they
  // were doing lives on another tile. Mirrors the cross-tile drag
  // focus-follows-tab UX.
  const migrateFocusAfterTabClose = (sourceTileId: string | undefined) => {
    if (!sourceTileId)
      return
    if (layoutStore.focusedTileId() !== sourceTileId)
      return
    if (tabStore.getTabsForTile(sourceTileId).length > 0)
      return
    const active = tabStore.activeTab()
    if (active?.tileId && active.tileId !== sourceTileId)
      layoutStore.setFocusedTile(active.tileId)
  }

  /**
   * Identify the workspace that owns `tab` for a cross-workspace
   * close (sidebar middle-click on a tab in workspace B while the
   * UI is on workspace A). Returns null when the tab belongs to the
   * active workspace or isn't tracked by any cached snapshot.
   *
   * For sidebar closes, the tab record itself comes from the
   * registry snapshot of its workspace — the active `tabStore` is
   * scoped to the visible workspace and doesn't know about it.
   * Without this lookup the close path dispatches to
   * `agentOps.handleAgentClose` / `termOps.handleTerminalClose`, both
   * of which look up the worker_id via
   * `tabStore.getAgentTab` / `tabStore.getTerminalTab` and bail when
   * the lookup returns nothing — so the worker-side agent / terminal
   * keeps running even though the CRDT tab is tombstoned, and the
   * sidebar still shows the row from the stale snapshot.
   */
  const ownerWorkspaceFor = (tab: Tab): string | null => {
    const active = getActiveWorkspaceId()
    const key = tabKey(tab)
    const snap = registry.findContaining(s => s.tabs.some(t => tabKey(t) === key))
    if (!snap)
      return null
    if (snap.workspaceId === active)
      return null
    return snap.workspaceId
  }

  /**
   * Close a FILE tab with a pre-determined worktree action. Mirrors
   * the shape of `agentOps.handleAgentClose` / `termOps.handleTerminalClose`
   * so the three tab types follow the same pattern (sync local
   * cleanup + fire-and-forget worker RPC + toast on failure). The
   * worker drives the unified closeTabCommon flow on its side; the
   * `workspaceId` argument identifies the workspace that owns the tab
   * — needed for cross-workspace closes where the active workspace
   * differs from the one the tab lives in.
   */
  const handleFileClose = (tabId: string, workerId: string, workspaceId: string, worktreeAction: WorktreeAction) => {
    const orgId = getOrgId()
    if (!orgId || !workerId)
      return
    workerRpc.revokeFileTabPath(workerId, { orgId, tabId, worktreeAction })
      .then(resp => toastCloseFailure(resp.result))
      .catch((err) => {
        showWarnToast('Failed to close file', err)
      })
    // workspaceId is unused locally — passed only so the call site reads
    // symmetrically with closeTerminal (which embeds workspaceId in the
    // request). Touch it to silence the unused-arg lint without
    // bypassing the symmetry.
    void workspaceId
  }

  /**
   * Close an agent / terminal / file tab with a pre-determined worktree
   * action, skipping the inspect+confirm prompt that handleTabClose
   * runs. Used by the delete-branch flow where the user has already
   * chosen the worktree fate for the whole branch group, so re-prompting
   * per tab would be wrong UX. Also runs the focus migration +
   * floating-window cleanup that an ad-hoc dispatch from the dialog
   * would otherwise miss.
   *
   * Intentionally does NOT add to `closingTabKeys`: handleTabClose
   * removes the key in its `finally{}` right before calling this for
   * its commit phase, so adding here would leave the key set forever
   * for the normal close flow. The sidebar X button concurrent-click
   * window is bounded by handleAgentClose / handleTerminalClose /
   * revokeFileTabPath's own per-tab dedup on the worker side
   * (idempotent close).
   */
  const closeTabWithAction = (tab: Tab, worktreeAction: WorktreeAction) => {
    // Cross-workspace branch: the tab lives in an inactive workspace's
    // registry snapshot (DeleteBranchDialog on a non-active branch
    // row), so the active-tabStore-bound helpers below can't find it.
    // Mirror handleTabClose's cross-workspace path so the worker still
    // gets a close RPC AND the source snapshot drops the row — without
    // the registry write the inactive workspace's sidebar tree keeps
    // showing the closed tab until the user switches into it.
    const crossWorkspaceWsId = ownerWorkspaceFor(tab)
    if (crossWorkspaceWsId) {
      const workerId = tab.workerId ?? ''
      if (workerId) {
        if (tab.type === TabType.AGENT) {
          workerRpc.closeAgent(workerId, { agentId: tab.id, worktreeAction })
            .then(resp => toastCloseFailure(resp.result))
            .catch((err) => {
              showWarnToast('Failed to close agent', err)
            })
        }
        else if (tab.type === TabType.TERMINAL) {
          const orgId = getOrgId() ?? ''
          workerRpc.closeTerminal(workerId, {
            orgId,
            workspaceId: crossWorkspaceWsId,
            terminalId: tab.id,
            worktreeAction,
          })
            .then(resp => toastCloseFailure(resp.result))
            .catch((err) => {
              showWarnToast('Failed to close terminal', err)
            })
        }
        else if (tab.type === TabType.FILE) {
          handleFileClose(tab.id, workerId, crossWorkspaceWsId, worktreeAction)
        }
      }
      // tabStore.removeTab is a no-op for a cross-workspace tab (the
      // active store doesn't carry it) but still emits the CRDT
      // tombstone via the bridge — the projection drops it from peer
      // clients regardless of which workspace is locally active.
      tabStore.removeTab(tab.type, tab.id)
      registry.removeTab(crossWorkspaceWsId, tab)
      // Skip migrateFocusAfterTabClose / removeEmptyFloatingWindowForTile
      // — those operate on the ACTIVE layout, and the closed tab's
      // tileId belongs to the inactive workspace.
      return
    }

    if (tab.type === TabType.AGENT) {
      agentOps.handleAgentClose(tab.id, worktreeAction)
    }
    else if (tab.type === TabType.TERMINAL) {
      termOps.handleTerminalClose(tab.id, worktreeAction)
    }
    else if (tab.type === TabType.FILE) {
      // Mirrors handleAgentClose / handleTerminalClose: sync local
      // cleanup first so the tab disappears immediately, then the
      // fire-and-forget worker RPC. The worker drives closeTabCommon
      // server-side, so worktreeAction REMOVE actually removes the
      // worktree from disk once no other tabs reference it — matching
      // the AGENT / TERMINAL last-close behavior.
      tabStore.removeTab(tab.type, tab.id)
      const wsId = getActiveWorkspaceId() ?? ''
      if (tab.workerId)
        handleFileClose(tab.id, tab.workerId, wsId, worktreeAction)
    }
    else {
      return
    }
    migrateFocusAfterTabClose(tab.tileId)
    removeEmptyFloatingWindowForTile(tab.tileId)
  }

  /**
   * Close a tab. Returns true on success, false if the user cancelled the
   * last-tab/worktree confirmation prompt or an error aborted the close.
   * Auto-removes the parent floating window if this close empties it.
   *
   * The same flow applies to AGENT, TERMINAL, and FILE tabs: ask the
   * worker via inspectLastTabClose whether closing this tab would
   * empty its worktree (or its non-worktree branch with pending git
   * state), surface the confirmation dialog when needed, and dispatch
   * to closeTabWithAction with the user-chosen WorktreeAction. The
   * worker mirrors the symmetry server-side via closeTabCommon, so a
   * FILE-only worktree close goes through the same `git worktree
   * remove` pipeline as an AGENT- or TERMINAL-only one.
   */
  const handleTabClose = async (tab: Tab): Promise<boolean> => {
    const key = tabKey(tab)
    if (closingTabKeys().has(key))
      return false
    addClosingTabKey(key)

    // Decide phase: the tab stays visible (with a spinner) while we
    // await the worker's last-tab inspection and, if needed, the user's
    // dialog choice. This is the only phase that awaits; the commit
    // phase below mutates stores synchronously and fires the worker
    // close + hub unregister RPCs as fire-and-forget.
    //
    // Orphan-worker fallback: when the worker referenced by the tab
    // no longer exists / isn't reachable, the inspection RPC fails
    // with a NotFound-class connect error. Without the carve-out
    // below the user gets a "Failed to prepare tab close" toast and
    // the tab stays put — there's no way to clean up a stale row.
    // The CLI's `agent close` / `terminal close` does the same
    // fallback (`isWorkerUnreachable` in cmd/preflight.go); keep
    // these two predicates in sync.
    let worktreeAction: WorktreeAction = WorktreeAction.KEEP
    try {
      const workerId = tab.workerId ?? ''
      const status = await workerRpc.inspectLastTabClose(workerId, { tabType: tab.type, tabId: tab.id })
      if (status.shouldPrompt) {
        const choice = await askLastTabConfirmation(workerId, tab.type, tab.id, status)
        if (choice === 'cancel') {
          return false
        }
        if (choice === 'schedule-delete') {
          worktreeAction = WorktreeAction.REMOVE
          showInfoToast('Worktree will be removed')
        }
      }
    }
    catch (err) {
      if (!isWorkerUnreachable(err)) {
        showWarnToast('Failed to prepare tab close', err)
        return false
      }
      // Worker is gone for an existence/auth reason. We can't ask
      // it about worktree state, so skip the dialog and fall
      // through to commit. The downstream worker RPCs (closeAgent /
      // closeTerminal / revokeFileTabPath) are already fire-and-forget
      // — they'll fail with the same code, get caught, and just toast.
      // The CRDT tombstone still runs and removes the orphan row.
      showInfoToast('Worker is unreachable; removing the tab without closing it.')
    }
    finally {
      removeClosingTabKey(key)
    }

    // Commit phase: synchronous UI mutations first so the tab
    // disappears immediately, then fire-and-forget worker cleanup and
    // hub unregister. closeTabWithAction owns both halves for AGENT,
    // TERMINAL, and FILE (cross-workspace included), so handleTabClose
    // only has to forward the user's worktreeAction choice.
    closeTabWithAction(tab, worktreeAction)
    return true
  }

  const generateFileTabId = makeIdGenerator('file')
  const handleFileOpen = (path: string, openSource?: FileOpenSource) => {
    const ctx = getCurrentTabContext()
    if (!ctx.workerId)
      return

    const existingTab = tabStore.state.tabs.find(
      t => t.type === TabType.FILE && t.filePath === path && t.workerId === ctx.workerId,
    )
    if (existingTab) {
      tabStore.activateTab(existingTab.tileId ?? '', existingTab.type, existingTab.id)
      return
    }

    // Determine initial view mode based on open source.
    let fileViewMode: FileTab['fileViewMode'] = 'working'
    let fileDiffBase: FileTab['fileDiffBase']
    if (openSource === 'staged') {
      fileViewMode = 'unified-diff'
      fileDiffBase = 'head-vs-staged'
    }
    else if (openSource === 'changed' || openSource === 'unstaged') {
      fileViewMode = 'unified-diff'
      fileDiffBase = 'head-vs-working'
    }

    const fileName = basename(path) || path
    const tileId = layoutStore.focusedTileId()
    const afterKey = tabStore.getActiveTabKeyForTile(tileId)
    const tabId = generateFileTabId()
    tabStore.addTab({
      type: TabType.FILE,
      id: tabId,
      filePath: path,
      workerId: ctx.workerId,
      workingDir: ctx.workingDir,
      title: fileName,
      tileId,
      fileViewMode,
      fileDiffBase,
      fileOpenSource: openSource,
    }, { afterKey })
    tabStore.setActiveTabForTile(tileId, TabType.FILE, tabId)

    // E2EE worker-side path registration. The hub never sees the
    // path; the worker persists `(org_id, tab_id, workspace_id,
    // file_path)` and emits FileTabPathRegistered on the workspace's
    // private-event stream so peer clients populate their local
    // fileTabPaths cache. Fire-and-forget — failure here doesn't
    // unmount the locally-added tab; the user can retry by re-opening.
    const orgId = getOrgId()
    const wsId = getActiveWorkspaceId()
    if (orgId && wsId) {
      workerRpc.registerFileTabPath(ctx.workerId, {
        orgId,
        workspaceId: wsId,
        tabId,
        filePath: path,
      }).catch(() => {
        // Roll back the optimistic add so the user sees the failure
        // surface (and isn't left with a tab whose path peers can't
        // resolve).
        tabStore.removeTab(TabType.FILE, tabId)
      })
    }
  }

  // Reset file tree selection when active tab changes
  createEffect(() => {
    const _tab = activeTab()
    const ctx = getCurrentTabContext()
    setFileTreePath(ctx.workingDir || '~')
  })

  return {
    closingTabKeys,
    lastTabConfirmDialog,
    handleTabSelect,
    handleTabClose,
    closeTabWithAction,
    handleFileOpen,
    setIsTabEditing: (fn: () => boolean) => { isTabEditing = fn },
  }
}
