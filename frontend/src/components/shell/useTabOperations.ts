import type { useAgentOperations } from './useAgentOperations'
import type { useTerminalOperations } from './useTerminalOperations'
import type { CheckWorktreeStatusResponse } from '~/generated/leapmux/v1/git_pb'
import type { createAgentStore } from '~/stores/agent.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore, Tab } from '~/stores/tab.store'
import type { createTerminalStore } from '~/stores/terminal.store'
import { createEffect, createSignal } from 'solid-js'
import { gitClient } from '~/api/clients'
import { apiCallTimeout } from '~/api/transport'
import { getTerminalInstance } from '~/components/terminal/TerminalView'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { tabKey } from '~/stores/tab.store'

interface UseTabOperationsOpts {
  tabStore: ReturnType<typeof createTabStore>
  agentStore: ReturnType<typeof createAgentStore>
  terminalStore: ReturnType<typeof createTerminalStore>
  chatStore: ReturnType<typeof createChatStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  agentOps: ReturnType<typeof useAgentOperations>
  termOps: ReturnType<typeof useTerminalOperations>
  activeTab: () => Tab | undefined
  getCurrentTabContext: () => { workerId: string, workingDir: string, homeDir: string }
  persistLayout: () => void
  focusEditor: () => void
  getScrollState: () => { distFromBottom: number, atBottom: boolean } | undefined
  setFileTreePath: (path: string) => void
  pendingWorktreeChoiceRef: { current: 'keep' | 'remove' | null }
}

export function useTabOperations(opts: UseTabOperationsOpts) {
  const {
    tabStore,
    agentStore,
    terminalStore,
    chatStore,
    layoutStore,
    agentOps,
    termOps,
    activeTab,
    getCurrentTabContext,
    persistLayout,
    focusEditor,
    getScrollState,
    setFileTreePath,
    pendingWorktreeChoiceRef,
  } = opts

  const [closingTabKeys, setClosingTabKeys] = createSignal<Set<string>>(new Set())

  // Pre-close worktree confirmation dialog state
  const [worktreeConfirm, setWorktreeConfirm] = createSignal<{
    path: string
    id: string
    branchName: string
    resolve: (choice: 'cancel' | 'keep' | 'remove') => void
  } | null>(null)

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
    const prevAgentId = agentStore.state.activeAgentId
    if (prevAgentId) {
      const scrollState = getScrollState()
      if (scrollState !== undefined) {
        chatStore.saveViewportScroll(prevAgentId, scrollState.distFromBottom, scrollState.atBottom)
      }
    }

    tabStore.setActiveTab(tab.type, tab.id)
    if (tab.type === TabType.AGENT) {
      agentStore.setActiveAgent(tab.id)
      requestAnimationFrame(() => {
        if (isTabEditing())
          return
        focusEditor()
      })
    }
    else if (tab.type === TabType.TERMINAL) {
      terminalStore.setActiveTerminal(tab.id)
      requestAnimationFrame(() => {
        if (isTabEditing())
          return
        const instance = getTerminalInstance(tab.id)
        instance?.terminal.focus()
      })
    }
  }

  const askWorktreeConfirmation = (status: CheckWorktreeStatusResponse): Promise<'cancel' | 'keep' | 'remove'> => {
    return new Promise((resolve) => {
      setWorktreeConfirm({
        path: status.worktreePath,
        id: status.worktreeId,
        branchName: status.branchName,
        resolve,
      })
    })
  }

  const handleTabClose = async (tab: Tab) => {
    if (tab.type === TabType.FILE) {
      tabStore.removeTabFromTile(tab.type, tab.id, tab.tileId ?? '')
      persistLayout()
      return
    }

    try {
      const tabType = tab.type === TabType.AGENT ? TabType.AGENT : TabType.TERMINAL
      const status = await gitClient.checkWorktreeStatus({ tabType, tabId: tab.id }, apiCallTimeout())
      if (status.hasWorktree && status.isLastTab && status.isDirty) {
        const choice = await askWorktreeConfirmation(status)
        if (choice === 'cancel') {
          return
        }
        pendingWorktreeChoiceRef.current = choice
      }
    }
    catch {
      // If the pre-check fails, proceed with close (best-effort).
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
    }
    finally {
      removeClosingTabKey(key)
      pendingWorktreeChoiceRef.current = null
    }
  }

  let fileTabCounter = 0
  const handleFileOpen = (path: string) => {
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

    const fileName = path.split('/').pop() ?? path
    const tileId = layoutStore.focusedTileId()
    const tabId = `file-${++fileTabCounter}-${Date.now()}`
    tabStore.addTab({
      type: TabType.FILE,
      id: tabId,
      filePath: path,
      workerId: ctx.workerId,
      workingDir: ctx.workingDir,
      title: fileName,
      tileId,
    })
    tabStore.setActiveTabForTile(tileId, TabType.FILE, tabId)
    persistLayout()
  }

  // Reset file tree selection when active tab changes
  createEffect(() => {
    const _tab = activeTab()
    const ctx = getCurrentTabContext()
    setFileTreePath(ctx.workingDir || '~')
  })

  return {
    closingTabKeys,
    worktreeConfirm,
    setWorktreeConfirm,
    handleTabSelect,
    handleTabClose,
    handleFileOpen,
    setIsTabEditing: (fn: () => boolean) => { isTabEditing = fn },
  }
}
