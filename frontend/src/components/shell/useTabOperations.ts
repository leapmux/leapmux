import type { TabContext } from './tabContext'
import type { useAgentOperations } from './useAgentOperations'
import type { useTerminalOperations } from './useTerminalOperations'
import type { InspectLastTabCloseResponse } from '~/generated/leapmux/v1/git_pb'
import type { createAgentStore } from '~/stores/agent.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore, FileOpenSource, Tab } from '~/stores/tab.store'
import { batch, createEffect, createSignal } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { showInfoToast, showWarnToast } from '~/components/common/Toast'
import { getTerminalInstance } from '~/components/terminal/TerminalView'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { basename } from '~/lib/paths'
import { MAX_BACKGROUND_CHAT_MESSAGES } from '~/stores/chat.store'
import { tabKey } from '~/stores/tab.store'

interface UseTabOperationsOpts {
  tabStore: ReturnType<typeof createTabStore>
  agentStore: ReturnType<typeof createAgentStore>
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
}

export function useTabOperations(opts: UseTabOperationsOpts) {
  const {
    tabStore,
    agentStore,
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
    // happens while the previous tab is still visible.
    const prevAgentId = agentStore.state.activeAgentId
    const prevTab = activeTab()
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
      tabStore.setActiveTab(tab.type, tab.id)
      if (tab.tileId) {
        tabStore.setActiveTabForTile(tab.tileId, tab.type, tab.id)
      }
      if (tab.type === TabType.AGENT) {
        agentStore.setActiveAgent(tab.id)
      }
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

  const removeEmptyFloatingWindow = (tileId: string | undefined) => {
    if (!tileId || !floatingWindowStore)
      return
    const windowId = floatingWindowStore.getWindowForTile(tileId)
    if (windowId) {
      floatingWindowStore.removeIfEmpty(
        windowId,
        tId => tabStore.getTabsForTile(tId),
        layoutStore.focusedTileId(),
        tId => layoutStore.setFocusedTile(tId),
        layoutStore.getAllTileIds(),
      )
    }
  }

  const handleTabClose = async (tab: Tab) => {
    if (tab.type === TabType.FILE) {
      tabStore.removeTabFromTile(tab.type, tab.id, tab.tileId ?? '')
      removeEmptyFloatingWindow(tab.tileId)
      return
    }

    const key = tabKey(tab)
    if (closingTabKeys().has(key))
      return
    addClosingTabKey(key)

    // Decide phase: the tab stays visible (with a spinner) while we
    // await the worker's last-tab inspection and, if needed, the user's
    // dialog choice. This is the only phase that awaits; the commit
    // phase below mutates stores synchronously and fires the worker
    // close + hub unregister RPCs as fire-and-forget.
    let worktreeAction: WorktreeAction = WorktreeAction.KEEP
    try {
      const tabType = tab.type === TabType.AGENT ? TabType.AGENT : TabType.TERMINAL
      const workerId = tab.workerId ?? ''
      const status = await workerRpc.inspectLastTabClose(workerId, { tabType, tabId: tab.id })
      if (status.shouldPrompt) {
        const choice = await askLastTabConfirmation(workerId, tabType, tab.id, status)
        if (choice === 'cancel') {
          return
        }
        if (choice === 'schedule-delete') {
          worktreeAction = WorktreeAction.REMOVE
          showInfoToast('Worktree will be removed')
        }
      }
    }
    catch (err) {
      showWarnToast('Failed to prepare tab close', err)
      return
    }
    finally {
      removeClosingTabKey(key)
    }

    // Commit phase: synchronous UI mutations first so the tab
    // disappears immediately, then fire-and-forget worker cleanup and
    // hub unregister. handleCloseAgent/handleTerminalClose own both
    // halves (sync + fire-and-forget).
    if (tab.type === TabType.AGENT) {
      agentOps.handleCloseAgent(tab.id, worktreeAction)
    }
    else {
      // Instance disposal is owned by TerminalView's reactive effect,
      // which fires when removeTab evicts the tab from the store.
      termOps.handleTerminalClose(tab.id, worktreeAction)
    }
    removeEmptyFloatingWindow(tab.tileId)
  }

  let fileTabCounter = 0
  const handleFileOpen = (path: string, openSource?: FileOpenSource) => {
    const ctx = getCurrentTabContext()
    if (!ctx.workerId)
      return

    const existingTab = tabStore.state.tabs.find(
      t => t.type === TabType.FILE && t.filePath === path && t.workerId === ctx.workerId,
    )
    if (existingTab) {
      tabStore.setActiveTab(existingTab.type, existingTab.id)
      if (existingTab.tileId) {
        tabStore.setActiveTabForTile(existingTab.tileId, existingTab.type, existingTab.id)
      }
      return
    }

    // Determine initial view mode based on open source.
    let fileViewMode: Tab['fileViewMode'] = 'working'
    let fileDiffBase: Tab['fileDiffBase']
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
    const tabId = `file-${++fileTabCounter}-${Date.now()}`
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
