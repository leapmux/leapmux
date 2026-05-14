import type { TabContext } from './tabContext'
import type { useAgentOperations } from './useAgentOperations'
import type { useTerminalOperations } from './useTerminalOperations'
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

  type LastTabCloseChoice = 'cancel' | 'schedule-delete' | 'close-anyway'

  const [lastTabConfirm, setLastTabConfirm] = createSignal<
    (InspectLastTabCloseResponse & { resolve: (choice: LastTabCloseChoice) => void, onPush: () => Promise<void> }) | null
  >(null)

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
      const onPush = async () => {
        await workerRpc.pushBranchForClose(workerId, { tabType, tabId })
        const updated = await workerRpc.inspectLastTabClose(workerId, { tabType, tabId })
        setLastTabConfirm(prev => prev ? { ...updated, resolve: prev.resolve, onPush: prev.onPush } : null)
        showInfoToast('Branch pushed successfully')
      }
      setLastTabConfirm({ ...status, resolve, onPush })
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
   * `agentOps.handleCloseAgent` / `termOps.handleTerminalClose`, both
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
   * Close a tab. Returns true on success, false if the user cancelled the
   * last-tab/worktree confirmation prompt or an error aborted the close.
   * Auto-removes the parent floating window if this close empties it.
   */
  const handleTabClose = async (tab: Tab): Promise<boolean> => {
    const crossWorkspaceWsId = ownerWorkspaceFor(tab)

    if (tab.type === TabType.FILE) {
      tabStore.removeTab(tab.type, tab.id)
      if (crossWorkspaceWsId)
        registry.removeTab(crossWorkspaceWsId, tab)
      migrateFocusAfterTabClose(tab.tileId)
      removeEmptyFloatingWindowForTile(tab.tileId)
      // E2EE worker cleanup: drop the (tab_id → path) row so a future
      // tab_id with the same value (after recycling) doesn't see a
      // stale path. Fire-and-forget — the CRDT tombstone is already
      // optimistic via tabStore.removeTab; the worker call is the
      // belt-and-suspenders cleanup.
      const orgId = getOrgId()
      if (orgId && tab.workerId) {
        workerRpc.revokeFileTabPath(tab.workerId, { orgId, tabId: tab.id }).catch(() => {})
      }
      return true
    }

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
      // closeTerminal) are already fire-and-forget — they'll fail
      // with the same code, get caught, and just toast. The CRDT
      // tombstone still runs and removes the orphan row.
      showInfoToast('Worker is unreachable; removing the tab without closing the agent.')
    }
    finally {
      removeClosingTabKey(key)
    }

    if (crossWorkspaceWsId) {
      // Cross-workspace close: the active-workspace store handlers
      // (`agentOps.handleCloseAgent` / `termOps.handleTerminalClose`)
      // resolve worker_id via the active stores and skip the worker
      // RPC when it's empty. Drive the close directly with values
      // from the tab snapshot, then update the owner workspace's
      // registry so the sidebar row disappears immediately.
      const workerId = tab.workerId ?? ''
      if (workerId) {
        if (tab.type === TabType.AGENT) {
          workerRpc.closeAgent(workerId, { agentId: tab.id, worktreeAction })
            .then(resp => toastCloseFailure(resp.result))
            .catch((err) => {
              showWarnToast('Failed to close agent', err)
            })
        }
        else {
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
      }
      // Emit the CRDT TombstoneTab op so the projection drops the
      // tab on every connected client (tabStore.removeTab is a
      // local no-op when the tab isn't in the active store but the
      // CRDT emit still runs).
      tabStore.removeTab(tab.type, tab.id)
      registry.removeTab(crossWorkspaceWsId, tab)
      return true
    }

    // Commit phase: synchronous UI mutations first so the tab
    // disappears immediately, then fire-and-forget worker cleanup and
    // hub unregister. handleCloseAgent/handleTerminalClose own both
    // halves (sync + fire-and-forget).
    if (tab.type === TabType.AGENT) {
      agentOps.handleCloseAgent(tab.id, worktreeAction)
    }
    else {
      termOps.handleTerminalClose(tab.id, worktreeAction)
    }
    migrateFocusAfterTabClose(tab.tileId)
    removeEmptyFloatingWindowForTile(tab.tileId)
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
    lastTabConfirm,
    setLastTabConfirm,
    handleTabSelect,
    handleTabClose,
    handleFileOpen,
    setIsTabEditing: (fn: () => boolean) => { isTabEditing = fn },
  }
}
