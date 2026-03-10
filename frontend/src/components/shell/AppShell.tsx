import type { ParentComponent } from 'solid-js'
import type { KeyPinConfirmState } from './AppShellDialogs'
import type { SidebarElementsOpts } from './SidebarElements'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import { useLocation, useNavigate, useParams, useSearchParams } from '@solidjs/router'
import { createEffect, createMemo, createSignal, on, Show } from 'solid-js'
import { workerClient, workspaceClient } from '~/api/clients'
import { agentLoadingTimeoutMs } from '~/api/transport'
import { channelManager, listAgents, listTerminals, moveTabWorkspace, setConfirmKeyPin, setGetUserId } from '~/api/workerRpc'
import { NotFoundPage } from '~/components/common/NotFoundPage'
import { isWorkspaceMutatable } from '~/components/shell/sectionUtils'
import { WorkerSettingsDialog } from '~/components/workers/WorkerSettingsDialog'
import { useAuth } from '~/context/AuthContext'
import { useOrg } from '~/context/OrgContext'
import { usePreferences } from '~/context/PreferencesContext'
import { useWorkspace } from '~/context/WorkspaceContext'
import { SectionType } from '~/generated/leapmux/v1/section_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { useIsMobile } from '~/hooks/useIsMobile'
import { useWorkspaceConnection } from '~/hooks/useWorkspaceConnection'
import { createAgentStore } from '~/stores/agent.store'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import { createLayoutStore, getAllTileIds } from '~/stores/layout.store'
import { createSectionStore } from '~/stores/section.store'
import { createTabStore, tabKey } from '~/stores/tab.store'
import { createTerminalStore } from '~/stores/terminal.store'
import { createWorkerChannelStatusStore } from '~/stores/workerChannelStatus.store'
import { createWorkerInfoStore } from '~/stores/workerInfo.store'
import { createWorkspaceStore } from '~/stores/workspace.store'
import { createWorkspaceStoreRegistry } from '~/stores/workspaceStoreRegistry'
import * as styles from './AppShell.css'
import { AppShellDialogs } from './AppShellDialogs'
import { DesktopLayout } from './DesktopLayout'
import { MobileLayout } from './MobileLayout'
import { createLeftSidebarElement, createRightSidebarElement } from './SidebarElements'
import { createTileRenderer } from './TileRenderer'
import { useAgentOperations } from './useAgentOperations'
import { useTabOperations } from './useTabOperations'
import { useTabPersistence } from './useTabPersistence'
import { useTerminalOperations } from './useTerminalOperations'
import { useTileDragDrop } from './useTileDragDrop'
import { useWorkspaceLoader } from './useWorkspaceLoader'
import { useWorkspaceRestore } from './useWorkspaceRestore'

export const AppShell: ParentComponent = (props) => {
  const auth = useAuth()
  const workspace = useWorkspace()
  const org = useOrg()
  const preferences = usePreferences()
  const params = useParams<{ orgSlug: string, workspaceId?: string }>()
  const [searchParams, setSearchParams] = useSearchParams()
  const location = useLocation()
  const navigate = useNavigate()

  const workspaceStore = createWorkspaceStore()
  const sectionStore = createSectionStore()
  const registry = createWorkspaceStoreRegistry()

  // Active stores: these stable instances are used throughout AppShell.
  // On workspace switch, useWorkspaceRestore saves their state to the old
  // bundle in the registry and restores from the new bundle (or fetches).
  const agentStore = createAgentStore()
  const chatStore = createChatStore()
  const terminalStore = createTerminalStore()
  const tabStore = createTabStore()
  const controlStore = createControlStore()
  const agentSessionStore = createAgentSessionStore()
  const layoutStore = createLayoutStore()
  const gitFileStatusStore = createGitFileStatusStore()
  const [fileTreePath, setFileTreePath] = createSignal('')
  const [showNewWorkspace, setShowNewWorkspace] = createSignal(false)
  const [preselectedWorkerId, setPreselectedWorkerId] = createSignal<string | undefined>(undefined)
  const [newWorkspaceTargetSectionId, setNewWorkspaceTargetSectionId] = createSignal<string | null>(null)
  const [workspaceLoading, setWorkspaceLoading] = createSignal(true)
  const [showResumeDialog, setShowResumeDialog] = createSignal(false)
  const [showNewAgentDialog, setShowNewAgentDialog] = createSignal(false)
  const [showNewTerminalDialog, setShowNewTerminalDialog] = createSignal(false)
  const [newAgentLoading, setNewAgentLoading] = createSignal(false)
  const [newTerminalLoading, setNewTerminalLoading] = createSignal(false)
  const [newShellLoading, setNewShellLoading] = createSignal(false)
  const settingsLoading = createLoadingSignal(agentLoadingTimeoutMs(true))
  const [confirmDeleteWs, setConfirmDeleteWs] = createSignal<{ workspaceId: string, resolve: (confirmed: boolean) => void } | null>(null)
  const [confirmArchiveWs, setConfirmArchiveWs] = createSignal<{ workspaceId: string, resolve: (confirmed: boolean) => void } | null>(null)
  const [keyPinConfirm, setKeyPinConfirm] = createSignal<KeyPinConfirmState | null>(null)

  // Worker section state
  const workerInfoStore = createWorkerInfoStore()
  const workerChannelStatusStore = createWorkerChannelStatusStore(channelManager)
  const [workers, setWorkers] = createSignal<Worker[]>([])
  const [deregisterTarget, setDeregisterTarget] = createSignal<Worker | null>(null)

  // Fetch workers when org changes
  createEffect(() => {
    const orgId = org.orgId()
    if (!orgId)
      return
    void (async () => {
      try {
        const resp = await workerClient.listWorkers({ orgId })
        setWorkers(resp.workers)
        workerChannelStatusStore.setWorkerIds(resp.workers.map(w => w.id))
        for (const w of resp.workers) {
          if (w.online) {
            workerInfoStore.fetchWorkerInfo(w.id)
          }
        }
      }
      catch {
        // Best effort — sidebar will show empty workers list.
      }
    })()
  })

  // Register E2EE channel callbacks (module-level singletons in workerRpc.ts).
  setConfirmKeyPin((workerId, expectedFingerprint, actualFingerprint) =>
    new Promise((resolve) => {
      setKeyPinConfirm({ workerId, expectedFingerprint, actualFingerprint, resolve })
    }),
  )
  setGetUserId(() => auth.user()?.id ?? '')

  // Mobile layout state
  const isMobile = useIsMobile()
  const [leftSidebarOpen, setLeftSidebarOpen] = createSignal(false)
  const [rightSidebarOpen, setRightSidebarOpen] = createSignal(false)
  const toggleLeftSidebar = () => {
    setLeftSidebarOpen(prev => !prev)
    setRightSidebarOpen(false)
  }
  const toggleRightSidebar = () => {
    setRightSidebarOpen(prev => !prev)
    setLeftSidebarOpen(false)
  }
  const closeAllSidebars = () => {
    setLeftSidebarOpen(false)
    setRightSidebarOpen(false)
  }

  // Shared turn-end signal: bumped when an agent turn ends.
  // Drives sound playback, git file status refresh, and directory tree refresh.
  const [turnEndTrigger, setTurnEndTrigger] = createSignal(0)

  // Debounced turn-end handler
  const TURN_END_SOUND_COOLDOWN_MS = 10_000
  let lastSoundPlayedAt = 0
  // Late-bound ref: set once useTabOperations is initialized (after useWorkspaceConnection).
  let isAgentClosing: (agentId: string) => boolean = () => false
  const handleTurnEnd = (agentId: string) => {
    if (isAgentClosing(agentId))
      return
    setTurnEndTrigger(v => v + 1)
    const now = Date.now()
    if (now - lastSoundPlayedAt < TURN_END_SOUND_COOLDOWN_MS)
      return
    const sound = preferences.turnEndSound()
    if (sound === 'ding-dong') {
      lastSoundPlayedAt = now
      const audio = new Audio('/sounds/benkirb-electronic-doorbell-262895.mp3')
      audio.volume = preferences.turnEndSoundVolume() / 100
      audio.play().catch(() => {})
    }
  }

  // Streaming connection management
  useWorkspaceConnection({
    agentStore,
    chatStore,
    terminalStore,
    tabStore,
    controlStore,
    agentSessionStore,
    registry,
    settingsLoading,
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    getWorkerId: () => {
      // Derive workerId from active tab's agent/terminal store data.
      const tab = tabStore.activeTab()
      if (!tab)
        return ''
      if (tab.type === TabType.AGENT) {
        return agentStore.state.agents.find(a => a.id === tab.id)?.workerId ?? ''
      }
      if (tab.type === TabType.TERMINAL) {
        return terminalStore.state.terminals.find(t => t.id === tab.id)?.workerId ?? ''
      }
      return tab.workerId ?? ''
    },
    onTurnEnd: handleTurnEnd,
  })

  // Auto-open new workspace dialog from URL search params
  createEffect(() => {
    if (searchParams.newWorkspace === 'true') {
      setPreselectedWorkerId(searchParams.workerId as string | undefined)
      setShowNewWorkspace(true)
      setSearchParams({ newWorkspace: undefined, workerId: undefined }, { replace: true })
    }
  })

  // Detect if we're on a workspace route
  const isWorkspaceRoute = createMemo(() => {
    const path = location.pathname
    const orgPrefix = `/o/${params.orgSlug}`
    return path === orgPrefix || path === `${orgPrefix}/` || path.startsWith(`${orgPrefix}/workspace/`)
  })

  // True when the URL has a workspace ID but it doesn't exist in the loaded list
  const workspaceNotFound = createMemo(() => {
    if (!params.workspaceId)
      return false
    if (workspaceStore.state.loading)
      return false
    if (!org.orgId())
      return false
    return !workspaceStore.state.workspaces.some(w => w.id === params.workspaceId)
  })

  // Sync workspaceId from URL params to WorkspaceContext
  createEffect(() => {
    workspace.setActiveWorkspaceId(params.workspaceId ?? null)
  })

  // Workspace & section loading
  const { loadWorkspaces, loadSections, handleMoveSection, handleMoveSectionServer } = useWorkspaceLoader({
    getOrgId: () => org.orgId(),
    workspaceStore,
    sectionStore,
  })

  // Auto-activate workspace when navigating to org root with no workspace selected
  createEffect(() => {
    if (!isWorkspaceRoute())
      return
    if (params.workspaceId)
      return
    const workspaces = workspaceStore.state.workspaces
    if (workspaces.length === 0)
      return
    const savedId = sessionStorage.getItem('leapmux:activeWorkspace')
    const target = (savedId && workspaces.some(w => w.id === savedId))
      ? savedId
      : workspaces[0].id
    navigate(`/o/${params.orgSlug}/workspace/${target}`, { replace: true })
  })

  // Dynamic page title
  createEffect(() => {
    if (!isWorkspaceRoute())
      return
    const ws = workspaceStore.state.workspaces.find(s => s.id === workspace.activeWorkspaceId())
    if (ws) {
      document.title = `${ws.title || 'Untitled'} - LeapMux`
    }
    else {
      document.title = 'Dashboard - LeapMux'
    }
  })

  // Active workspace object
  const activeWorkspace = createMemo(() => {
    const id = workspace.activeWorkspaceId()
    if (!id)
      return null
    return workspaceStore.state.workspaces.find(s => s.id === id) ?? null
  })

  // Whether the active workspace is in an archived section
  const isActiveWorkspaceArchived = createMemo(() => {
    const wsId = workspace.activeWorkspaceId()
    if (!wsId)
      return false
    const sectionId = sectionStore.getSectionForWorkspace(wsId)
    if (!sectionId)
      return false
    const section = sectionStore.state.sections.find(s => s.id === sectionId)
    return section?.sectionType === SectionType.WORKSPACES_ARCHIVED
  })

  // Whether the active workspace can be mutated
  const isActiveWorkspaceMutatable = createMemo(() =>
    isWorkspaceMutatable(activeWorkspace(), auth.user()?.id ?? '', isActiveWorkspaceArchived()),
  )

  // Active tab derived state
  const activeTab = createMemo(() => tabStore.activeTab())
  const activeTabType = createMemo(() => activeTab()?.type ?? null)

  // Get worker, working directory, and home directory from the currently active tab
  const getCurrentTabContext = (): { workerId: string, workingDir: string, homeDir: string } => {
    const tab = activeTab()
    if (!tab)
      return { workerId: '', workingDir: '', homeDir: '' }
    if (tab.type === TabType.AGENT) {
      const agent = agentStore.state.agents.find(a => a.id === tab.id)
      const workerId = agent?.workerId || ''
      return { workerId, workingDir: agent?.workingDir ?? '', homeDir: agent?.homeDir ?? '' }
    }
    else if (tab.type === TabType.FILE) {
      const dir = tab.workingDir || (tab.filePath ? tab.filePath.substring(0, tab.filePath.lastIndexOf('/')) || '/' : '')
      const homeDir = agentStore.state.agents.find(a => a.workerId === tab.workerId)?.homeDir ?? ''
      return { workerId: tab.workerId ?? '', workingDir: dir, homeDir }
    }
    else {
      const terminal = terminalStore.state.terminals.find(t => t.id === tab.id)
      const workerId = terminal?.workerId ?? ''
      const homeDir = agentStore.state.agents.find(a => a.workerId === workerId)?.homeDir ?? ''
      return { workerId, workingDir: terminal?.workingDir ?? '', homeDir }
    }
  }

  // Refresh git file status when a turn ends.
  createEffect(on(
    () => turnEndTrigger(),
    (_, prev) => {
      if (prev === undefined)
        return
      const ctx = getCurrentTabContext()
      if (ctx.workerId && ctx.workingDir) {
        gitFileStatusStore.refresh(ctx.workerId, ctx.workingDir)
      }
    },
  ))

  // Sync git file status store to tab-level diff stats so the workspace
  // tab tree stays consistent with the directory tree after refreshes.
  createEffect(() => {
    const files = gitFileStatusStore.state.files
    const repoRoot = gitFileStatusStore.state.repoRoot
    if (!repoRoot)
      return
    let added = 0
    let deleted = 0
    for (const f of files) {
      added += f.linesAdded + f.stagedLinesAdded
      deleted += f.linesDeleted + f.stagedLinesDeleted
    }
    for (const tab of tabStore.state.tabs) {
      if (tab.type !== TabType.AGENT)
        continue
      const agent = agentStore.state.agents.find(a => a.id === tab.id)
      if (!agent?.workingDir)
        continue
      if (agent.workingDir === repoRoot || agent.workingDir.startsWith(`${repoRoot}/`)) {
        tabStore.updateTab(TabType.AGENT, tab.id, { gitDiffAdded: added, gitDiffDeleted: deleted })
      }
    }
  })

  // Get working directory and home directory from the MRU agent tab
  const getMruAgentContext = (): { workingDir: string, homeDir: string } => {
    const agentPrefix = `${TabType.AGENT}:`
    const mruKey = tabStore.state.mruOrder.find(k => k.startsWith(agentPrefix))
    if (!mruKey)
      return { workingDir: '', homeDir: '' }
    const agentId = mruKey.slice(agentPrefix.length)
    const agent = agentStore.state.agents.find(a => a.id === agentId)
    return { workingDir: agent?.workingDir ?? '', homeDir: agent?.homeDir ?? '' }
  }

  // Mutable refs for editor/scroll callbacks
  const focusEditorRef: { current: (() => void) | undefined } = { current: undefined }
  const getScrollStateRef: { current: (() => { distFromBottom: number, atBottom: boolean } | undefined) | undefined } = { current: undefined }
  const forceScrollToBottomRef: { current: (() => void) | undefined } = { current: undefined }
  const [centerPanelHeight, setCenterPanelHeight] = createSignal(0)

  // Tab persistence (layout save, sessionStorage effects)
  const { persistLayout, persistMultiLayout } = useTabPersistence({
    tabStore,
    layoutStore,
    registry,
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    getOrgId: () => org.orgId(),
    activeWorkspace,
    workspaceLoading,
  })

  // Shared pending worktree choice (used by agentOps, termOps, and tabOps)
  const pendingWorktreeChoiceRef: { current: 'keep' | 'remove' | null } = { current: null }

  // Agent operations hook
  const agentOps = useAgentOperations({
    agentStore,
    chatStore,
    controlStore,
    tabStore,
    layoutStore,
    settingsLoading,
    isActiveWorkspaceMutatable,
    activeWorkspace,
    getCurrentTabContext,
    pendingWorktreeChoice: () => pendingWorktreeChoiceRef.current,
    setShowNewAgentDialog,
    setNewAgentLoading,
    setShowResumeDialog,
    persistLayout,
    focusEditor: () => focusEditorRef.current?.(),
    forceScrollToBottom: () => forceScrollToBottomRef.current?.(),
  })

  // Terminal operations hook
  const termOps = useTerminalOperations({
    org,
    tabStore,
    terminalStore,
    layoutStore,
    activeWorkspace,
    isActiveWorkspaceMutatable,
    getCurrentTabContext,
    setShowNewTerminalDialog,
    setNewTerminalLoading,
    setNewShellLoading,
    pendingWorktreeChoice: () => pendingWorktreeChoiceRef.current,
    persistLayout,
  })

  // Tab operations (select, close, file open, worktree confirm)
  const tabOps = useTabOperations({
    tabStore,
    agentStore,
    terminalStore,
    chatStore,
    layoutStore,
    agentOps,
    termOps,
    activeTab,
    getCurrentTabContext,
    focusEditor: () => focusEditorRef.current?.(),
    getScrollState: () => getScrollStateRef.current?.(),
    setFileTreePath,
    pendingWorktreeChoiceRef,
  })
  // Bind the closing-agent check now that tabOps is available.
  isAgentClosing = (agentId: string) =>
    tabOps.closingTabKeys().has(tabKey({ type: TabType.AGENT, id: agentId }))

  // Workspace restore (load agents/terminals/tabs/layout on workspace change)
  useWorkspaceRestore({
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    getOrgId: () => org.orgId(),
    agentStore,
    terminalStore,
    tabStore,
    layoutStore,
    chatStore,
    controlStore,
    agentSessionStore,
    registry,
    setWorkspaceLoading,
  })

  // Tile drag-and-drop
  const tileDrag = useTileDragDrop({ tabStore, layoutStore, persistLayout })

  // Cross-workspace tab move handler (drag a tab to another workspace in the sidebar)
  const handleCrossWorkspaceMove = (targetWorkspaceId: string, draggedKey: string, sourceWorkspaceId?: string) => {
    const activeWsId = workspace.activeWorkspaceId()
    if (!activeWsId)
      return

    // Resolve the actual source and target workspace IDs.
    const resolvedSourceWsId = sourceWorkspaceId ?? activeWsId
    const resolvedTargetWsId = targetWorkspaceId === '__active__' ? activeWsId : targetWorkspaceId

    // No-op if source and target are the same.
    if (resolvedSourceWsId === resolvedTargetWsId)
      return

    // Find the tab in the source: either active tabStore or registry snapshot.
    const isSourceActive = resolvedSourceWsId === activeWsId
    const isTargetActive = resolvedTargetWsId === activeWsId

    let tab: ReturnType<typeof tabStore.state.tabs.find>
    if (isSourceActive) {
      tab = tabStore.state.tabs.find(t => tabKey(t) === draggedKey)
    }
    else {
      const sourceSnap = registry.get(resolvedSourceWsId)
      tab = sourceSnap?.tabs.tabs.find((t: any) => `${t.type}:${t.id}` === draggedKey)
    }
    if (!tab)
      return

    // Determine the worker for this tab
    let workerId = tab.workerId ?? ''
    if (!workerId) {
      if (tab.type === TabType.AGENT) {
        workerId = agentStore.state.agents.find(a => a.id === tab.id)?.workerId ?? ''
      }
      else if (tab.type === TabType.TERMINAL) {
        workerId = terminalStore.state.terminals.find(t => t.id === tab.id)?.workerId ?? ''
      }
    }

    // Remove the tab from the source (optimistic UI update).
    if (isSourceActive) {
      tabStore.removeTab(tab.type, tab.id)
    }
    else {
      const sourceSnap = registry.get(resolvedSourceWsId)
      if (sourceSnap) {
        sourceSnap.tabs.tabs = sourceSnap.tabs.tabs.filter((t: any) => `${t.type}:${t.id}` !== draggedKey)
        // Also remove the agent/terminal from the source snapshot.
        if (tab.type === TabType.AGENT) {
          sourceSnap.agents = sourceSnap.agents.filter(a => a.id !== tab.id)
        }
        else if (tab.type === TabType.TERMINAL) {
          sourceSnap.terminals = sourceSnap.terminals.filter(t => t.id !== tab.id)
        }
        registry.set(resolvedSourceWsId, { ...sourceSnap })
      }
    }

    // Add the tab to the target (optimistic UI update).
    if (isTargetActive) {
      // When moving from a non-active workspace, the tab's tileId may not exist
      // in the active workspace's layout. Use the focused tile as a fallback.
      const activeTileId = !isSourceActive
        ? (layoutStore.focusedTileId() ?? tab.tileId)
        : tab.tileId
      tabStore.addTab({
        type: tab.type,
        id: tab.id,
        title: tab.title,
        workerId: tab.workerId,
        filePath: tab.filePath,
        position: tab.position,
        tileId: activeTileId,
        displayMode: tab.displayMode,
        fileViewMode: tab.fileViewMode,
        fileDiffBase: tab.fileDiffBase,
        gitBranch: tab.gitBranch,
        gitOriginUrl: tab.gitOriginUrl,
        gitDiffAdded: tab.gitDiffAdded,
        gitDiffDeleted: tab.gitDiffDeleted,
      })
    }
    else {
      const targetSnap = registry.get(resolvedTargetWsId)
      if (targetSnap) {
        // Use a valid tileId from the target workspace's layout.
        const targetTileIds = getAllTileIds(targetSnap.layout.root)
        const targetTileId = targetSnap.layout.focusedTileId ?? targetTileIds[0] ?? ''
        const newTab = { ...tab, tileId: targetTileId }
        const key = `${newTab.type}:${newTab.id}`
        targetSnap.tabs.tabs = [...targetSnap.tabs.tabs, newTab]
        targetSnap.tabs.activeTabKey = key
        targetSnap.tabs.mruOrder = [key, ...targetSnap.tabs.mruOrder]
        if (targetTileId) {
          targetSnap.tabs.tileActiveTabKeys = {
            ...targetSnap.tabs.tileActiveTabKeys,
            [targetTileId]: key,
          }
        }
        // Move the agent/terminal data to the target snapshot.
        if (tab.type === TabType.AGENT) {
          const agent = agentStore.state.agents.find(a => a.id === tab.id)
          if (agent && !targetSnap.agents.some(a => a.id === tab.id)) {
            targetSnap.agents = [...targetSnap.agents, agent]
          }
        }
        else if (tab.type === TabType.TERMINAL) {
          const term = terminalStore.state.terminals.find(t => t.id === tab.id)
          if (term && !targetSnap.terminals.some(t => t.id === tab.id)) {
            targetSnap.terminals = [...targetSnap.terminals, term]
          }
        }
        registry.set(resolvedTargetWsId, { ...targetSnap })
      }
    }

    // For FILE tabs, update sessionStorage entries.
    if (tab.type === TabType.FILE) {
      try {
        // Add to target workspace's sessionStorage
        const targetKey = `leapmux:localTabs:${resolvedTargetWsId}`
        const existing = JSON.parse(sessionStorage.getItem(targetKey) ?? '[]') as Array<Record<string, unknown>>
        existing.push({
          type: tab.type,
          id: tab.id,
          filePath: tab.filePath,
          workerId: tab.workerId,
          position: tab.position,
          tileId: tab.tileId,
          title: tab.title,
          displayMode: tab.displayMode,
          fileViewMode: tab.fileViewMode,
          fileDiffBase: tab.fileDiffBase,
        })
        sessionStorage.setItem(targetKey, JSON.stringify(existing))

        // Remove from source workspace's sessionStorage
        const sourceKey = `leapmux:localTabs:${resolvedSourceWsId}`
        const sourceExisting = JSON.parse(sessionStorage.getItem(sourceKey) ?? '[]') as Array<Record<string, unknown>>
        const filtered = sourceExisting.filter((t: any) => !(t.type === tab!.type && t.id === tab!.id))
        if (filtered.length > 0)
          sessionStorage.setItem(sourceKey, JSON.stringify(filtered))
        else
          sessionStorage.removeItem(sourceKey)
      }
      catch { /* quota */ }
    }

    // Tell the worker to reassign the tab's workspace, then persist
    // both workspaces to the hub. The RPC must complete before persist
    // so that a subsequent listAgents returns the agent under the new
    // workspace. Persist without debounce — cross-workspace moves are
    // discrete actions that must survive an immediate page refresh.
    const rpcDone = (workerId && tab.type !== TabType.FILE)
      ? moveTabWorkspace(workerId, {
          tabType: tab.type,
          tabId: tab.id,
          newWorkspaceId: resolvedTargetWsId,
        }).catch(() => {})
      : Promise.resolve()

    rpcDone.then(() => persistMultiLayout(true))
  }

  // Lazy-load tabs for a non-active workspace when its tree is expanded.
  const handleExpandWorkspace = (workspaceId: string) => {
    const snap = registry.get(workspaceId)
    if (snap?.tabsLoaded)
      return
    const currentOrgId = org.orgId()
    if (!currentOrgId)
      return

    workspaceClient.listTabs({ orgId: currentOrgId, workspaceId }).then(async (tabsResp) => {
      const workerIds = new Set<string>()
      for (const t of tabsResp.tabs) {
        if (t.workerId)
          workerIds.add(t.workerId)
      }

      const [agentResults, terminalResults] = await Promise.all([
        Promise.all([...workerIds].map(async (wid) => {
          try {
            return (await listAgents(wid, { workspaceId })).agents
          }
          catch { return [] }
        })),
        Promise.all([...workerIds].map(async (wid) => {
          try {
            return { workerId: wid, terminals: (await listTerminals(wid, { orgId: currentOrgId, workspaceId })).terminals }
          }
          catch { return { workerId: wid, terminals: [] as Awaited<ReturnType<typeof listTerminals>>['terminals'] } }
        })),
      ])

      const allAgents = agentResults.flat()
      const tabs: import('~/stores/tab.store').Tab[] = []

      for (const a of allAgents) {
        tabs.push({
          type: TabType.AGENT,
          id: a.id,
          title: a.title || undefined,
          workerId: a.workerId,
          workingDir: a.workingDir,
          gitBranch: a.gitStatus?.branch || undefined,
          gitOriginUrl: a.gitStatus?.originUrl || undefined,
        })
      }

      for (const { workerId, terminals } of terminalResults) {
        for (const t of terminals) {
          tabs.push({
            type: TabType.TERMINAL,
            id: t.terminalId,
            title: t.title || undefined,
            workerId,
            workingDir: t.workingDir || undefined,
            gitBranch: t.gitBranch || undefined,
            gitOriginUrl: t.gitOriginUrl || undefined,
          })
        }
      }

      const existing = registry.get(workspaceId)
      registry.set(workspaceId, {
        workspaceId,
        tabs: { tabs, activeTabKey: existing?.tabs.activeTabKey ?? null, mruOrder: [] },
        layout: existing?.layout ?? { root: { type: 'leaf', id: 'default' } },
        agents: allAgents,
        terminals: existing?.terminals ?? [],
        restored: false,
        tabsLoaded: true,
      })
    }).catch(() => {})
  }

  // Active agent todos (for right sidebar To-dos pane)
  const activeTodos = createMemo(() => {
    const id = agentStore.state.activeAgentId
    if (!id)
      return []
    return chatStore.getTodos(id)
  })

  const showTodos = createMemo(() => activeTabType() === TabType.AGENT && activeTodos().length > 0)

  // Workspace selection navigates to URL
  const handleSelectWorkspace = (id: string) => {
    closeAllSidebars()
    navigate(`/o/${params.orgSlug}/workspace/${id}`)
  }

  // Handle workspace deletion
  const handleDeleteWorkspace = (deletedId: string, nextWorkspaceId: string | null) => {
    if (workspace.activeWorkspaceId() !== deletedId)
      return
    tabStore.clear()
    if (nextWorkspaceId) {
      navigate(`/o/${params.orgSlug}/workspace/${nextWorkspaceId}`)
    }
    else {
      navigate(`/o/${params.orgSlug}`)
    }
  }

  // Promise-based confirmation callbacks for workspace operations
  const handleConfirmDeleteWorkspace = (workspaceId: string): Promise<boolean> =>
    new Promise((resolve) => {
      setConfirmDeleteWs({ workspaceId, resolve })
    })

  const handleConfirmArchiveWorkspace = (workspaceId: string): Promise<boolean> =>
    new Promise((resolve) => {
      setConfirmArchiveWs({ workspaceId, resolve })
    })

  // Post-archive cleanup
  const handlePostArchiveWorkspace = (workspaceId: string) => {
    if (workspace.activeWorkspaceId() === workspaceId) {
      for (const agent of agentStore.state.agents) controlStore.clearAgent(agent.id)
    }
  }

  // Tile renderer (tab bars, tile content, editor panel)
  const tileRenderer = createTileRenderer({
    tabStore,
    agentStore,
    chatStore,
    terminalStore,
    controlStore,
    layoutStore,
    agentSessionStore,
    settingsLoading,
    agentOps,
    termOps,
    hasMultipleTiles: tileDrag.hasMultipleTiles,
    isActiveWorkspaceMutatable,
    isActiveWorkspaceArchived,
    activeWorkspace,
    getCurrentTabContext,
    getMruAgentContext,
    handleTabSelect: tabOps.handleTabSelect,
    handleTabClose: tabOps.handleTabClose,
    setIsTabEditing: tabOps.setIsTabEditing,
    persistLayout,
    closingTabKeys: tabOps.closingTabKeys,
    newAgentLoading,
    newTerminalLoading,
    newShellLoading,
    isMobile,
    toggleLeftSidebar,
    toggleRightSidebar,
    setShowResumeDialog,
    setShowNewAgentDialog,
    setShowNewTerminalDialog,
    focusEditorRef,
    getScrollStateRef,
    forceScrollToBottomRef,
    gitFileStatusStore,
  })

  // Sidebar element factories
  // Use getters for reactive values so that LeftSidebar/RightSidebar props
  // remain reactive when accessed through the intermediate opts object.
  const sidebarOpts = (): SidebarElementsOpts => ({
    get workspaces() { return workspaceStore.state.workspaces },
    get activeWorkspaceId() { return workspace.activeWorkspaceId() },
    sectionStore,
    tabStore,
    registry,
    loadSections,
    onSelectWorkspace: handleSelectWorkspace,
    onNewWorkspace: (sectionId: string | null) => {
      setNewWorkspaceTargetSectionId(sectionId)
      setShowNewWorkspace(true)
    },
    onRefreshWorkspaces: () => loadWorkspaces(),
    onDeleteWorkspace: handleDeleteWorkspace,
    onConfirmDelete: handleConfirmDeleteWorkspace,
    onConfirmArchive: handleConfirmArchiveWorkspace,
    onPostArchiveWorkspace: handlePostArchiveWorkspace,
    getCurrentTabContext,
    getMruAgentContext,
    get fileTreePath() { return fileTreePath() },
    onFileSelect: setFileTreePath,
    onFileOpen: tabOps.handleFileOpen,
    get isActiveWorkspaceArchived() { return isActiveWorkspaceArchived() },
    get showTodos() { return showTodos() },
    get activeTodos() { return activeTodos() },
    termOps,
    gitStatusStore: gitFileStatusStore,
    get turnEndTrigger() { return turnEndTrigger() },
    get activeFilePath() {
      const active = tabStore.activeTab()
      return active?.type === TabType.FILE ? active.filePath : undefined
    },
    get hasActiveFileTab() {
      const active = tabStore.activeTab()
      return active?.type === TabType.FILE
    },
    get workers() { return workers() },
    workerInfoFn: workerInfoStore.workerInfo,
    channelStatusFn: workerChannelStatusStore.getStatus,
    onDeregisterWorker: (worker: Worker) => setDeregisterTarget(worker),
    onTabClick: (type: number, id: string) => {
      const tabType = type as TabType
      tabStore.setActiveTab(tabType, id)
      const tab = tabStore.state.tabs.find(t => t.type === tabType && t.id === id)
      if (tab?.tileId) {
        tabStore.setActiveTabForTile(tab.tileId, tabType, id)
      }
      if (tabType === TabType.AGENT) {
        agentStore.setActiveAgent(id)
      }
      else if (tabType === TabType.TERMINAL) {
        terminalStore.setActiveTerminal(id)
      }
    },
    onExpandWorkspace: handleExpandWorkspace,
  })

  // Refresh git status only when workerId or workingDir actually changes
  // (not on every tab switch within the same worker context).
  createEffect(on(
    () => {
      const ctx = getCurrentTabContext()
      return `${ctx.workerId}\0${ctx.workingDir}`
    },
    () => {
      const ctx = getCurrentTabContext()
      if (ctx.workerId && ctx.workingDir) {
        gitFileStatusStore.refresh(ctx.workerId, ctx.workingDir)
      }
      else {
        gitFileStatusStore.clear()
      }
    },
  ))

  return (
    <>
      <Show when={workspaceNotFound()}>
        <NotFoundPage
          message="The workspace you're looking for doesn't exist or you don't have access."
          linkHref={`/o/${params.orgSlug}`}
          linkText="Go to Dashboard"
        />
      </Show>
      <Show
        when={isWorkspaceRoute() && !workspaceNotFound()}
        fallback={<Show when={!workspaceNotFound()}><div class={styles.fullWindow}>{props.children}</div></Show>}
      >
        <div style={{ '--mono-font-family': preferences.monoFontFamily(), '--ui-font-family': preferences.uiFontFamily(), 'display': 'contents' }}>
          <Show
            when={!isMobile()}
            fallback={(
              <MobileLayout
                sectionStore={sectionStore}
                onMoveSection={handleMoveSection}
                onMoveSectionServer={handleMoveSectionServer}
                leftSidebarOpen={leftSidebarOpen()}
                rightSidebarOpen={rightSidebarOpen()}
                closeAllSidebars={closeAllSidebars}
                leftSidebarElement={createLeftSidebarElement(sidebarOpts())}
                rightSidebarElement={createRightSidebarElement(sidebarOpts())}
                tabBarElement={tileRenderer.tabBarElement()}
                tileContent={tileRenderer.renderTileContent(layoutStore.focusedTileId())}
                editorPanel={
                  tileRenderer.focusedAgentId() && !isActiveWorkspaceArchived()
                  && <tileRenderer.FocusedAgentEditorPanel containerHeight={0} />
                }
              />
            )}
          >
            <DesktopLayout
              sectionStore={sectionStore}
              layoutStore={layoutStore}
              onMoveSection={handleMoveSection}
              onMoveSectionServer={handleMoveSectionServer}
              activeWorkspaceId={workspace.activeWorkspaceId()}
              activeWorkspace={activeWorkspace}
              workspaceLoading={workspaceLoading()}
              getInProgressSectionId={() => sectionStore.getInProgressSection()?.id ?? null}
              onNewWorkspace={() => {
                setNewWorkspaceTargetSectionId(sectionStore.getInProgressSection()?.id ?? null)
                setShowNewWorkspace(true)
              }}
              setCenterPanelHeight={setCenterPanelHeight}
              onIntraTileReorder={tileDrag.handleIntraTileReorder}
              onCrossTileMove={tileDrag.handleCrossTileMove}
              onCrossWorkspaceMove={handleCrossWorkspaceMove}
              lookupTileIdForTab={tileDrag.lookupTileIdForTab}
              renderDragOverlay={tileDrag.renderDragOverlay}
              renderTile={tileRenderer.renderTile}
              onRatioChange={(splitId, ratios) => {
                layoutStore.updateRatios(splitId, ratios)
                persistLayout()
              }}
              createLeftSidebar={displayOpts => createLeftSidebarElement(sidebarOpts(), displayOpts)}
              createRightSidebar={displayOpts => createRightSidebarElement(sidebarOpts(), displayOpts)}
              editorPanel={
                tileRenderer.focusedAgentId() && !isActiveWorkspaceArchived()
                && <tileRenderer.FocusedAgentEditorPanel containerHeight={centerPanelHeight()} />
              }
            />
          </Show>
        </div>
      </Show>

      <AppShellDialogs
        showResumeDialog={showResumeDialog()}
        setShowResumeDialog={setShowResumeDialog}
        showNewAgentDialog={showNewAgentDialog()}
        setShowNewAgentDialog={setShowNewAgentDialog}
        showNewTerminalDialog={showNewTerminalDialog()}
        setShowNewTerminalDialog={setShowNewTerminalDialog}
        showNewWorkspace={showNewWorkspace()}
        setShowNewWorkspace={setShowNewWorkspace}
        preselectedWorkerId={preselectedWorkerId()}
        setPreselectedWorkerId={setPreselectedWorkerId}
        newWorkspaceTargetSectionId={newWorkspaceTargetSectionId()}
        setNewWorkspaceTargetSectionId={setNewWorkspaceTargetSectionId}
        confirmDeleteWs={confirmDeleteWs()}
        setConfirmDeleteWs={setConfirmDeleteWs}
        confirmArchiveWs={confirmArchiveWs()}
        setConfirmArchiveWs={setConfirmArchiveWs}
        worktreeConfirm={tabOps.worktreeConfirm()}
        setWorktreeConfirm={tabOps.setWorktreeConfirm}
        keyPinConfirm={keyPinConfirm()}
        setKeyPinConfirm={setKeyPinConfirm}
        activeWorkspace={activeWorkspace}
        getCurrentTabContext={getCurrentTabContext}
        agentOps={agentOps}
        agentStore={agentStore}
        tabStore={tabStore}
        terminalStore={terminalStore}
        layoutStore={layoutStore}
        workspaceStore={workspaceStore}
        persistLayout={persistLayout}
        focusEditor={() => focusEditorRef.current?.()}
        orgSlug={params.orgSlug}
        loadWorkspaces={loadWorkspaces}
        navigate={path => navigate(path)}
      />

      <Show when={deregisterTarget()}>
        {target => (
          <WorkerSettingsDialog
            worker={target()}
            onClose={() => setDeregisterTarget(null)}
            onDeregistered={() => {
              setWorkers(prev => prev.filter(w => w.id !== target().id))
              workerChannelStatusStore.setWorkerIds(workers().map(w => w.id))
              setDeregisterTarget(null)
            }}
          />
        )}
      </Show>
    </>
  )
}
