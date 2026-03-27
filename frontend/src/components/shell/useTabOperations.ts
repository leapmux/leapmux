import type { useAgentOperations } from './useAgentOperations'
import type { useTerminalOperations } from './useTerminalOperations'
import type { InspectLastTabCloseResponse } from '~/generated/leapmux/v1/git_pb'
import type { createAgentStore } from '~/stores/agent.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore, FileOpenSource, Tab } from '~/stores/tab.store'
import type { createTerminalStore } from '~/stores/terminal.store'
import { batch, createEffect, createSignal } from 'solid-js'
import * as workerRpc from '~/api/workerRpc'
import { showInfoToast, showWarnToast } from '~/components/common/Toast'
import { getTerminalInstance } from '~/components/terminal/TerminalView'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { tabKey } from '~/stores/tab.store'

interface UseTabOperationsOpts {
  tabStore: ReturnType<typeof createTabStore>
  agentStore: ReturnType<typeof createAgentStore>
  terminalStore: ReturnType<typeof createTerminalStore>
  chatStore: ReturnType<typeof createChatStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore?: ReturnType<typeof createFloatingWindowStore>
  agentOps: ReturnType<typeof useAgentOperations>
  termOps: ReturnType<typeof useTerminalOperations>
  activeTab: () => Tab | undefined
  getCurrentTabContext: () => { workerId: string, workingDir: string, homeDir: string }
  focusEditor: () => void
  getScrollState: () => { distFromBottom: number, atBottom: boolean } | undefined
  setFileTreePath: (path: string) => void
}

export function useTabOperations(opts: UseTabOperationsOpts) {
  const {
    tabStore,
    agentStore,
    terminalStore,
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
      if (tab.type === TabType.AGENT) {
        agentStore.setActiveAgent(tab.id)
      }
      else if (tab.type === TabType.TERMINAL) {
        terminalStore.setActiveTerminal(tab.id)
      }
    })

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
          await workerRpc.scheduleWorktreeDeletion(workerId, { worktreeId: status.worktreeId })
          showInfoToast('Worktree deletion scheduled')
        }
      }
    }
    catch (err) {
      showWarnToast('Failed to prepare tab close', err)
      return
    }

    const key = tabKey(tab)
    addClosingTabKey(key)
    try {
      if (tab.type === TabType.AGENT) {
        await agentOps.handleCloseAgent(tab.id)
      }
      else {
        const instance = getTerminalInstance(tab.id)
        if (instance) {
          instance.dispose()
        }
        await termOps.handleTerminalClose(tab.id)
      }
      removeEmptyFloatingWindow(tab.tileId)
    }
    finally {
      removeClosingTabKey(key)
    }
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

    const fileName = path.split('/').pop() ?? path
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
