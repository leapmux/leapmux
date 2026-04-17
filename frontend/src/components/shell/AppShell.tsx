import type { ParentComponent } from 'solid-js'
import type { KeyPinConfirmState } from './AppShellDialogs'
import type { SidebarElementsOpts } from './SidebarElements'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { Tab } from '~/stores/tab.store'
import { useLocation, useNavigate, useParams, useSearchParams } from '@solidjs/router'
import { createEffect, createMemo, createSignal, on, Show, untrack } from 'solid-js'
import { workerClient, workspaceClient } from '~/api/clients'
import { agentLoadingTimeoutMs } from '~/api/transport'
import { channelManager, listAgents, listTerminals, moveTabWorkspace, renameAgent, setConfirmKeyPin, setGetUserId } from '~/api/workerRpc'
import { NotFoundPage } from '~/components/common/NotFoundPage'
import { showWarnToast } from '~/components/common/Toast'
import { isWorkspaceMutatable } from '~/components/shell/sectionUtils'
import { AddTunnelDialog } from '~/components/workers/AddTunnelDialog'
import { WorkerSettingsDialog } from '~/components/workers/WorkerSettingsDialog'
import { useAuth } from '~/context/AuthContext'
import { useOrg } from '~/context/OrgContext'
import { usePreferences } from '~/context/PreferencesContext'
import { TunnelProvider } from '~/context/TunnelContext'
import { useWorkspace } from '~/context/WorkspaceContext'
import { HubControlEvent } from '~/generated/leapmux/v1/channel_pb'
import { GitFileStatusCode } from '~/generated/leapmux/v1/common_pb'
import { SectionType } from '~/generated/leapmux/v1/section_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { useChatAutoFocus } from '~/hooks/useChatAutoFocus'
import { useIsMobile } from '~/hooks/useIsMobile'
import { useShortcuts } from '~/hooks/useShortcuts'
import { useWorkspaceConnection } from '~/hooks/useWorkspaceConnection'
import { createLogger } from '~/lib/logger'
import { printConsoleBanner } from '~/lib/systemInfo'
import { createAgentStore } from '~/stores/agent.store'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import { createLayoutStore, getAllTileIds } from '~/stores/layout.store'
import { createSectionStore } from '~/stores/section.store'
import { createTabStore, protoToTerminalTab, tabKey } from '~/stores/tab.store'
import { createTunnelStore } from '~/stores/tunnel.store'
import { createWorkerChannelStatusStore } from '~/stores/workerChannelStatus.store'
import { createWorkerInfoStore } from '~/stores/workerInfo.store'
import { createWorkspaceStore } from '~/stores/workspace.store'
import { createWorkspaceStoreRegistry } from '~/stores/workspaceStoreRegistry'
import * as styles from './AppShell.css'
import { AppShellDialogs } from './AppShellDialogs'
import { CustomTitlebar } from './CustomTitlebar'
import * as titlebarStyles from './CustomTitlebar.css'
import { DesktopLayout } from './DesktopLayout'
import { FloatingWindowLayer } from './FloatingWindowLayer'
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

const log = createLogger('AppShell')
let turnEndAudio: HTMLAudioElement | undefined

export const AppShell: ParentComponent = (props) => {
  const auth = useAuth()
  const workspace = useWorkspace()
  const org = useOrg()
  const preferences = usePreferences()
  const params = useParams<{ orgSlug: string, workspaceId?: string }>()
  const [searchParams, setSearchParams] = useSearchParams()
  const location = useLocation()
  const navigate = useNavigate()

  printConsoleBanner()

  const workspaceStore = createWorkspaceStore()
  const sectionStore = createSectionStore()
  const registry = createWorkspaceStoreRegistry()

  // Active stores: these stable instances are used throughout AppShell.
  // On workspace switch, useWorkspaceRestore saves their state to the old
  // bundle in the registry and restores from the new bundle (or fetches).
  const agentStore = createAgentStore()
  const chatStore = createChatStore()
  const tabStore = createTabStore()
  const controlStore = createControlStore()
  const agentSessionStore = createAgentSessionStore()
  const layoutStore = createLayoutStore()
  const floatingWindowStore = createFloatingWindowStore()
  const gitFileStatusStore = createGitFileStatusStore()
  const [fileTreePath, setFileTreePath] = createSignal('')
  const [showNewWorkspace, setShowNewWorkspace] = createSignal(false)
  const [preselectedWorkerId, setPreselectedWorkerId] = createSignal<string | undefined>(undefined)
  const [newWorkspaceTargetSectionId, setNewWorkspaceTargetSectionId] = createSignal<string | null>(null)
  const [workspaceLoading, setWorkspaceLoading] = createSignal(true)
  const [showNewAgentDialog, setShowNewAgentDialog] = createSignal(false)
  const [showNewTerminalDialog, setShowNewTerminalDialog] = createSignal(false)
  const [newAgentLoadingProvider, setNewAgentLoadingProvider] = createSignal<AgentProvider | null>(null)
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
  const [addTunnelTarget, setAddTunnelTarget] = createSignal<Worker | null>(null)
  const tunnelStore = createTunnelStore()

  // Fetch workers list.
  async function fetchWorkers() {
    if (!org.orgId())
      return
    try {
      const resp = await workerClient.listWorkers({})
      setWorkers(resp.workers)
      for (const w of resp.workers) {
        if (w.online) {
          workerInfoStore.fetchWorkerInfo(w.id)
        }
      }
    }
    catch {
      // Best effort — sidebar will show empty workers list.
    }
  }

  // Fetch workers when org changes.
  createEffect(() => {
    org.orgId() // track
    void fetchWorkers()
  })

  // Re-fetch workers when the Hub sends a WorkersChanged control frame.
  channelManager.onHubControl((frame) => {
    if (frame.events.includes(HubControlEvent.WORKERS_CHANGED)) {
      void fetchWorkers()
    }
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

  // Lazily create the Audio element once on first mount.
  if (!turnEndAudio)
    turnEndAudio = new Audio('/sounds/benkirb-electronic-doorbell-262895.mp3')

  // Debounced turn-end handler
  const TURN_END_SOUND_COOLDOWN_MS = 60_000
  let lastSoundPlayedAt = 0
  // Late-bound ref: set once useTabOperations is initialized (after useWorkspaceConnection).
  let isAgentClosing: (agentId: string) => boolean = () => false
  const handleTurnEnd = (agentId: string, numToolUses?: number) => {
    if (isAgentClosing(agentId))
      return
    // Always bump the trigger (drives git status and directory tree refresh),
    // but skip the audible notification for trivial single-exchange turns.
    setTurnEndTrigger(v => v + 1)
    if (numToolUses !== undefined && numToolUses === 0)
      return
    const now = Date.now()
    if (now - lastSoundPlayedAt < TURN_END_SOUND_COOLDOWN_MS)
      return
    const sound = preferences.turnEndSound()
    if (sound === 'ding-dong') {
      lastSoundPlayedAt = now
      turnEndAudio!.currentTime = 0
      turnEndAudio!.volume = preferences.turnEndSoundVolume() / 100
      turnEndAudio!.play().catch(() => {})
    }
  }

  // Streaming connection management
  useWorkspaceConnection({
    agentStore,
    chatStore,
    tabStore,
    controlStore,
    agentSessionStore,
    registry,
    settingsLoading,
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    getWorkerId: () => {
      const tileId = layoutStore.focusedTileId()
      const tab = tileId ? tabStore.getActiveTabForTile(tileId) : null
      if (!tab)
        return ''
      if (tab.type === TabType.AGENT) {
        return agentStore.state.agents.find(a => a.id === tab.id)?.workerId ?? ''
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
    isWorkspaceMutatable(activeWorkspace() ?? undefined, auth.user()?.id ?? '', isActiveWorkspaceArchived()),
  )

  // Active tab derived state
  const activeTab = createMemo(() => {
    const tileId = layoutStore.focusedTileId()
    return tileId ? tabStore.getActiveTabForTile(tileId) : null
  })
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
      const workerId = tab.workerId ?? ''
      const homeDir = agentStore.state.agents.find(a => a.workerId === workerId)?.homeDir ?? ''
      return { workerId, workingDir: tab.workingDir ?? '', homeDir }
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
  // Use untrack for tab/agent reads so this effect only re-runs when the
  // git store changes — not when tabs change due to a workspace switch,
  // which would apply stale git data from the previous workspace.
  createEffect(() => {
    const files = gitFileStatusStore.state.files
    const repoRoot = gitFileStatusStore.state.repoRoot
    const originUrl = gitFileStatusStore.state.originUrl
    const currentBranch = gitFileStatusStore.state.currentBranch
    if (!repoRoot)
      return
    let added = 0
    let deleted = 0
    let untracked = 0
    for (const f of files) {
      if (f.unstagedStatus === GitFileStatusCode.UNTRACKED) {
        untracked++
      }
      else {
        added += f.linesAdded + f.stagedLinesAdded
        deleted += f.linesDeleted + f.stagedLinesDeleted
      }
    }
    const gitFields = {
      gitDiffAdded: added,
      gitDiffDeleted: deleted,
      gitDiffUntracked: untracked,
      gitOriginUrl: originUrl || undefined,
      gitBranch: currentBranch || undefined,
    }
    for (const tab of untrack(() => tabStore.state.tabs)) {
      if (tab.type === TabType.AGENT) {
        const agent = untrack(() => agentStore.state.agents.find(a => a.id === tab.id))
        if (!agent?.workingDir)
          continue
        if (agent.workingDir === repoRoot || agent.workingDir.startsWith(`${repoRoot}/`)) {
          tabStore.updateTab(TabType.AGENT, tab.id, gitFields)
        }
      }
      else if (tab.type === TabType.TERMINAL) {
        const workingDir = tab.workingDir
        if (!workingDir)
          continue
        if (workingDir === repoRoot || workingDir.startsWith(`${repoRoot}/`)) {
          tabStore.updateTab(TabType.TERMINAL, tab.id, gitFields)
        }
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

  const [leftSidebarVisible, setLeftSidebarVisible] = createSignal(true)
  const [rightSidebarVisible, setRightSidebarVisible] = createSignal(true)

  // Mutable refs for editor/scroll callbacks
  const toggleLeftSidebarRef: { current: (() => void) | undefined } = { current: undefined }
  const toggleRightSidebarRef: { current: (() => void) | undefined } = { current: undefined }
  const focusEditorRef: { current: (() => void) | undefined } = { current: undefined }
  const getScrollStateRef: { current: (() => { distFromBottom: number, atBottom: boolean } | undefined) | undefined } = { current: undefined }
  const forceScrollToBottomRef: { current: (() => void) | undefined } = { current: undefined }
  const [centerPanelHeight, setCenterPanelHeight] = createSignal(0)

  // Tab persistence (layout save, sessionStorage effects)
  const { persistLayout, persistMultiLayout } = useTabPersistence({
    tabStore,
    layoutStore,
    floatingWindowStore,
    registry,
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    getOrgId: () => org.orgId(),
    activeWorkspace,
    workspaceLoading,
  })

  // Agent operations hook
  const agentOps = useAgentOperations({
    agentStore,
    agentSessionStore,
    chatStore,
    controlStore,
    tabStore,
    layoutStore,
    settingsLoading,
    isActiveWorkspaceMutatable,
    activeWorkspace,
    getCurrentTabContext,
    setShowNewAgentDialog,
    setNewAgentLoadingProvider,
    persistLayout,
    focusEditor: () => focusEditorRef.current?.(),
    forceScrollToBottom: () => forceScrollToBottomRef.current?.(),
  })

  // Terminal operations hook
  const termOps = useTerminalOperations({
    org,
    tabStore,
    layoutStore,
    activeWorkspace,
    isActiveWorkspaceMutatable,
    getCurrentTabContext,
    setShowNewTerminalDialog,
    setNewTerminalLoading,
    setNewShellLoading,
    persistLayout,
  })

  // Tab operations (select, close, file open, worktree confirm)
  const tabOps = useTabOperations({
    tabStore,
    agentStore,
    chatStore,
    layoutStore,
    floatingWindowStore,
    agentOps,
    termOps,
    activeTab: () => activeTab() ?? undefined,
    getCurrentTabContext,
    focusEditor: () => focusEditorRef.current?.(),
    getScrollState: () => getScrollStateRef.current?.(),
    setFileTreePath,
  })
  // Bind the closing-agent check now that tabOps is available.
  isAgentClosing = (agentId: string) =>
    tabOps.closingTabKeys().has(tabKey({ type: TabType.AGENT, id: agentId }))

  // Workspace restore (load agents/terminals/tabs/layout on workspace change)
  useWorkspaceRestore({
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    getOrgId: () => org.orgId(),
    agentStore,
    tabStore,
    layoutStore,
    floatingWindowStore,
    chatStore,
    controlStore,
    agentSessionStore,
    registry,
    setWorkspaceLoading,
  })

  // Tile drag-and-drop
  const tileDrag = useTileDragDrop({ tabStore, layoutStore, floatingWindowStore, persistLayout })

  const focusTile = (tileId: string) => {
    const windowId = floatingWindowStore.getWindowForTile(tileId)
    if (windowId)
      floatingWindowStore.setFocusedTile(windowId, tileId)
    layoutStore.setFocusedTile(tileId)
  }

  // --- Floating window tab movement operations ---
  const handleDetachTab = (tab: Tab) => {
    const sourceTileId = tab.tileId
    const { tileId } = floatingWindowStore.addWindow()
    tabStore.moveTabToTile(tabKey(tab), tileId)
    tabStore.setActiveTabForTile(tileId, tab.type, tab.id)
    // Close the source tile if it's now empty and the main layout has multiple tiles
    if (sourceTileId && tabStore.getTabsForTile(sourceTileId).length === 0) {
      const mainTileIds = layoutStore.getAllTileIds()
      if (mainTileIds.length > 1) {
        layoutStore.closeTile(sourceTileId)
      }
    }
    focusTile(tileId)
    persistLayout()
  }

  const handleAttachTab = (tab: Tab) => {
    const sourceTileId = tab.tileId
    if (!sourceTileId)
      return

    const sourceWindowId = floatingWindowStore.getWindowForTile(sourceTileId)
    if (!sourceWindowId)
      return

    const targetTileId = layoutStore.getAllTileIds()[0]
    if (!targetTileId)
      return

    tabStore.moveTabToTile(tabKey(tab), targetTileId)
    tabStore.setActiveTabForTile(targetTileId, tab.type, tab.id)
    layoutStore.setFocusedTile(targetTileId)
    floatingWindowStore.removeIfEmpty(
      sourceWindowId,
      tId => tabStore.getTabsForTile(tId),
      layoutStore.focusedTileId(),
      tId => layoutStore.setFocusedTile(tId),
      layoutStore.getAllTileIds(),
    )
    persistLayout()
  }

  const handleToggleFloatingTab = () => {
    const tileId = layoutStore.focusedTileId()
    const tab = tileId ? tabStore.getActiveTabForTile(tileId) : null
    if (!tab)
      return
    if (floatingWindowStore.getWindowForTile(tileId))
      handleAttachTab(tab)
    else
      handleDetachTab(tab)
  }

  const handleCloseFloatingWindow = (windowId: string) => {
    const tileIds = floatingWindowStore.getWindowTileIds(windowId)
    // Close all tabs in the window
    for (const tId of tileIds) {
      const tileTabs = tabStore.getTabsForTile(tId)
      for (const t of tileTabs) {
        void tabOps.handleTabClose(t)
      }
    }
    const windowTileIdSet = new Set(tileIds)
    floatingWindowStore.removeWindow(windowId)
    // Reset focus to a main layout tile if it was on the removed window
    if (windowTileIdSet.has(layoutStore.focusedTileId())) {
      const mainTileIds = layoutStore.getAllTileIds()
      if (mainTileIds.length > 0) {
        layoutStore.setFocusedTile(mainTileIds[0])
      }
    }
    persistLayout()
  }

  const handleActivateFloatingWindow = (windowId: string) => {
    const tileId = floatingWindowStore.getWindow(windowId)?.focusedTileId
    if (tileId)
      focusTile(tileId)
  }

  // Cross-workspace tab move handler (drag a tab to another workspace in the sidebar)
  const handleCrossWorkspaceMove = (targetWorkspaceId: string, draggedKey: string, sourceWorkspaceId?: string, targetTileId?: string) => {
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

    let tab: Tab | undefined
    if (isSourceActive) {
      tab = tabStore.state.tabs.find(t => tabKey(t) === draggedKey)
    }
    else {
      const sourceSnap = registry.get(resolvedSourceWsId)
      tab = sourceSnap?.tabs.find(t => tabKey(t) === draggedKey)
    }
    if (!tab)
      return

    // Determine the worker for this tab
    let workerId = tab.workerId ?? ''
    if (!workerId && tab.type === TabType.AGENT) {
      workerId = agentStore.state.agents.find(a => a.id === tab.id)?.workerId ?? ''
    }

    // Remove the tab from the source (optimistic UI update).
    if (isSourceActive) {
      tabStore.removeTab(tab.type, tab.id)
    }
    else {
      registry.removeTab(resolvedSourceWsId, tab)
    }

    // If the source floating window is now empty, remove it.
    if (isSourceActive) {
      const srcTileId = tab.tileId
      if (srcTileId) {
        const srcWindowId = floatingWindowStore.getWindowForTile(srcTileId)
        if (srcWindowId) {
          floatingWindowStore.removeIfEmpty(
            srcWindowId,
            tId => tabStore.getTabsForTile(tId),
            layoutStore.focusedTileId(),
            tId => layoutStore.setFocusedTile(tId),
            layoutStore.getAllTileIds(),
          )
        }
      }
    }

    if (isTargetActive) {
      // Use the explicit target tile if provided (e.g. sidebar tab dropped on
      // a specific floating window tile). Otherwise fall back to the focused tile.
      const activeTileId = targetTileId
        ?? (!isSourceActive ? (layoutStore.focusedTileId() ?? tab.tileId) : tab.tileId)
      tabStore.addTab({ ...tab, tileId: activeTileId })
      if (activeTileId)
        focusTile(activeTileId)
    }
    else {
      // Get or create a snapshot for the target workspace.
      // If we create a new one, mark it as NOT tabsLoaded so that
      // saveMultiLayout won't include it (which would overwrite the
      // hub's full tab list with our partial view).
      const targetSnap = registry.get(resolvedTargetWsId) ?? {
        workspaceId: resolvedTargetWsId,
        tabs: [],
        activeTabKey: null,
        layout: {
          root: { type: 'leaf' as const, id: 'tile-1' },
          focusedTileId: 'tile-1',
        },
        agents: [],
        restored: false,
        tabsLoaded: false,
      }

      // Use a valid tileId from the target workspace's layout.
      const targetTileIds = getAllTileIds(targetSnap.layout.root)
      const targetTileId = targetSnap.layout.focusedTileId ?? targetTileIds[0] ?? ''
      const newTab = { ...tab, tileId: targetTileId }
      const key = tabKey(newTab)
      const movedAgent = tab.type === TabType.AGENT
        ? agentStore.state.agents.find(a => a.id === tab.id)
        : undefined
      const nextAgents = movedAgent && !targetSnap.agents.some(a => a.id === tab.id)
        ? [...targetSnap.agents, movedAgent]
        : targetSnap.agents
      registry.set(resolvedTargetWsId, {
        ...targetSnap,
        tabs: [...targetSnap.tabs, newTab],
        activeTabKey: key,
        mruOrder: [key, ...(targetSnap.mruOrder ?? [])],
        tileActiveTabKeys: targetTileId
          ? { ...(targetSnap.tileActiveTabKeys ?? {}), [targetTileId]: key }
          : targetSnap.tileActiveTabKeys,
        agents: nextAgents,
      })
    }

    // For FILE tabs, update sessionStorage entries.
    if (tab.type === TabType.FILE) {
      try {
        // Add to target workspace's sessionStorage
        const targetKey = `leapmux:localTabs:${resolvedTargetWsId}`
        const existing = JSON.parse(sessionStorage.getItem(targetKey) ?? '[]') as Array<Record<string, unknown>>
        existing.push({ ...tab })
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
    // workspace.
    const rpcDone = (workerId && tab.type !== TabType.FILE)
      ? moveTabWorkspace(workerId, {
          tabType: tab.type,
          tabId: tab.id,
          newWorkspaceId: resolvedTargetWsId,
        })
      : Promise.resolve()

    rpcDone.then(async () => {
      // If the target snapshot was newly created (not fully loaded),
      // fetch the target workspace's existing tabs from the hub and
      // merge them so saveMultiLayout sends the complete tab list
      // (hub does DELETE + INSERT, so partial saves lose existing tabs).
      const currentOrgId = org.orgId()
      const targetSnap = registry.get(resolvedTargetWsId)
      if (currentOrgId && targetSnap && !targetSnap.tabsLoaded) {
        try {
          const resp = await workspaceClient.listTabs({
            orgId: currentOrgId,
            workspaceId: resolvedTargetWsId,
          })
          const existingKeys = new Set(targetSnap.tabs.map(t => tabKey(t)))
          const extraTabs: Tab[] = []
          for (const t of resp.tabs) {
            const key = `${t.tabType}:${t.tabId}`
            if (!existingKeys.has(key)) {
              extraTabs.push({
                type: t.tabType as TabType,
                id: t.tabId,
                position: t.position,
                tileId: t.tileId || targetSnap.layout.focusedTileId || '',
                workerId: t.workerId,
              })
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
      persistMultiLayout()
    }).catch((err: unknown) => {
      // Worker RPC failed — revert the optimistic UI update.
      // Move the tab back to the source workspace.
      if (isTargetActive) {
        tabStore.removeTab(tab!.type, tab!.id)
      }
      else {
        registry.removeTab(resolvedTargetWsId, tab!)
      }

      // Add it back to the source workspace.
      if (isSourceActive) {
        tabStore.addTab(tab!)
      }
      else {
        const targetSnap = registry.get(resolvedTargetWsId)
        registry.update(resolvedSourceWsId, (sourceSnap) => {
          let nextAgents = sourceSnap.agents
          if (tab!.type === TabType.AGENT) {
            const agent = agentStore.state.agents.find(a => a.id === tab!.id)
              ?? targetSnap?.agents.find(a => a.id === tab!.id)
            if (agent && !sourceSnap.agents.some(a => a.id === tab!.id)) {
              nextAgents = [...sourceSnap.agents, agent]
            }
          }
          return { ...sourceSnap, tabs: [...sourceSnap.tabs, tab!], agents: nextAgents }
        })
      }

      showWarnToast('Failed to move tab', err)
    })
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
      // Group tab IDs by worker and type.
      const agentIdsByWorker = new Map<string, string[]>()
      const terminalIdsByWorker = new Map<string, string[]>()
      for (const t of tabsResp.tabs) {
        if (!t.workerId)
          continue
        if (t.tabType === TabType.AGENT) {
          const ids = agentIdsByWorker.get(t.workerId) ?? []
          ids.push(t.tabId)
          agentIdsByWorker.set(t.workerId, ids)
        }
        else if (t.tabType === TabType.TERMINAL) {
          const ids = terminalIdsByWorker.get(t.workerId) ?? []
          ids.push(t.tabId)
          terminalIdsByWorker.set(t.workerId, ids)
        }
      }

      const [agentResults, terminalResults] = await Promise.all([
        Promise.all(Array.from(agentIdsByWorker.entries(), async ([wid, tabIds]) => {
          try {
            return (await listAgents(wid, { tabIds })).agents
          }
          catch (err) {
            log.warn('failed to list agents from worker', { workerId: wid, tabIds, err })
            return []
          }
        })),
        Promise.all(Array.from(terminalIdsByWorker.entries(), async ([wid, tabIds]) => {
          try {
            return { workerId: wid, terminals: (await listTerminals(wid, { tabIds })).terminals }
          }
          catch (err) {
            log.warn('failed to list terminals from worker', { workerId: wid, tabIds, err })
            return { workerId: wid, terminals: null }
          }
        })),
      ])

      const allAgents = agentResults.flat()
      const anyTerminalFetchFailed = terminalResults.some(r => r.terminals === null)
      const tabs: Tab[] = []

      for (const a of allAgents) {
        tabs.push({
          type: TabType.AGENT,
          id: a.id,
          title: a.title || undefined,
          workerId: a.workerId,
          workingDir: a.workingDir,
          agentProvider: a.agentProvider,
          gitBranch: a.gitStatus?.branch || undefined,
          gitOriginUrl: a.gitStatus?.originUrl || undefined,
        })
      }

      for (const { workerId, terminals } of terminalResults) {
        if (terminals === null)
          continue
        for (const t of terminals) {
          tabs.push(protoToTerminalTab(workerId, t))
        }
      }

      const existing = registry.get(workspaceId)
      // When a terminal fetch fails, preserve the previous terminal tabs (if any)
      // so they don't disappear from the sidebar on a transient error. An empty
      // successful result means the worker truly has no terminals.
      if (anyTerminalFetchFailed && existing) {
        const existingTerminalIds = new Set(tabs.filter(t => t.type === TabType.TERMINAL).map(t => t.id))
        for (const t of existing.tabs) {
          if (t.type === TabType.TERMINAL && !existingTerminalIds.has(t.id))
            tabs.push(t)
        }
      }
      registry.set(workspaceId, {
        workspaceId,
        tabs,
        activeTabKey: existing?.activeTabKey ?? null,
        layout: existing?.layout ?? { root: { type: 'leaf', id: 'default' }, focusedTileId: null },
        agents: allAgents,
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
    newAgentLoadingProvider,
    newTerminalLoading,
    newShellLoading,
    isMobile,
    toggleLeftSidebar,
    toggleRightSidebar,
    setShowNewAgentDialog,
    setShowNewTerminalDialog,
    focusEditorRef,
    getScrollStateRef,
    forceScrollToBottomRef,
    gitFileStatusStore,
    floatingWindowStore,
    isFloatingWindowTile: (tileId: string) => !!floatingWindowStore.getWindowForTile(tileId),
    onDetachTab: handleDetachTab,
    onAttachTab: handleAttachTab,
  })

  useChatAutoFocus(() => tileRenderer.focusedAgentId())

  useShortcuts({
    tabStore,
    layoutStore,
    tabOps,
    agentOps,
    termOps,
    setShowNewAgentDialog,
    setShowNewTerminalDialog,
    setShowNewWorkspace,
    hasActiveWorkspace: () => activeWorkspace() !== null,
    toggleFloatingTab: handleToggleFloatingTab,
    toggleLeftSidebar: () => {
      if (isMobile()) {
        toggleLeftSidebar()
      }
      else {
        toggleLeftSidebarRef.current?.()
      }
    },
    toggleRightSidebar: () => toggleRightSidebarRef.current?.(),
    activeTabType,
    resolveFocusedTab: tileRenderer.resolveFocusedTab,
    splitFocusedTile: tileRenderer.splitFocusedTile,
    scrollFocusedTabPage: tileRenderer.scrollFocusedTabPage,
    writeToFocusedTerminal: tileRenderer.writeToFocusedTerminal,
    customKeybindings: preferences.customKeybindings,
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
    currentUserId: auth.user()?.id ?? '',
    onAddTunnel: (worker: Worker) => setAddTunnelTarget(worker),
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
    },
    tabItemOps: {
      onClose: tabOps.handleTabClose,
      onRename: (tab, title) => {
        tabStore.updateTabTitle(tab.type, tab.id, title)
        if (tab.type === TabType.AGENT) {
          const workerId = agentStore.state.agents.find(a => a.id === tab.id)?.workerId ?? ''
          renameAgent(workerId, { agentId: tab.id, title }).catch((err) => {
            showWarnToast('Failed to rename agent', err)
          })
        }
      },
      get closingKeys() { return tabOps.closingTabKeys() },
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
    <TunnelProvider store={tunnelStore}>
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
        <div style={{ '--mono-font-family': preferences.monoFontFamily(), '--ui-font-family': preferences.uiFontFamily(), 'position': 'relative', 'height': '100%', 'width': '100%' }}>
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
            <div class={titlebarStyles.titlebarLayout}>
              <CustomTitlebar
                onToggleLeftSidebar={() => toggleLeftSidebarRef.current?.()}
                onToggleRightSidebar={() => toggleRightSidebarRef.current?.()}
                leftSidebarVisible={leftSidebarVisible()}
                rightSidebarVisible={rightSidebarVisible()}
              />
              <div class={titlebarStyles.titlebarContent}>
                <DesktopLayout
                  setToggleLeftSidebar={fn => toggleLeftSidebarRef.current = fn}
                  setToggleRightSidebar={fn => toggleRightSidebarRef.current = fn}
                  setLeftSidebarVisible={setLeftSidebarVisible}
                  setRightSidebarVisible={setRightSidebarVisible}
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
                  onFileDrop={tileRenderer.handleFileDrop}
                  fileDropDisabled={tileRenderer.fileDropDisabled()}
                  editorPanel={(
                    tileRenderer.focusedAgentId() && !isActiveWorkspaceArchived()
                    && <tileRenderer.FocusedAgentEditorPanel containerHeight={centerPanelHeight()} />
                  )}
                  floatingWindowLayer={(
                    <FloatingWindowLayer
                      floatingWindowStore={floatingWindowStore}
                      tabStore={tabStore}
                      renderTile={tileRenderer.renderTile}
                      onRatioChange={(windowId, splitId, ratios) => {
                        floatingWindowStore.updateRatios(windowId, splitId, ratios)
                        persistLayout()
                      }}
                      onCloseWindow={handleCloseFloatingWindow}
                      onActivateWindow={handleActivateFloatingWindow}
                      onGeometryChange={persistLayout}
                      onFileDrop={tileRenderer.handleFileDrop}
                      fileDropDisabled={tileRenderer.fileDropDisabled()}
                    />
                  )}
                />
              </div>
            </div>
          </Show>
        </div>
      </Show>

      <AppShellDialogs
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
        lastTabConfirm={tabOps.lastTabConfirm()}
        setLastTabConfirm={tabOps.setLastTabConfirm}
        keyPinConfirm={keyPinConfirm()}
        setKeyPinConfirm={setKeyPinConfirm}
        activeWorkspace={activeWorkspace}
        getCurrentTabContext={getCurrentTabContext}
        agentOps={agentOps}
        agentStore={agentStore}
        tabStore={tabStore}
        layoutStore={layoutStore}
        workspaceStore={workspaceStore}
        persistLayout={persistLayout}
        focusEditor={() => focusEditorRef.current?.()}
        orgSlug={params.orgSlug}
        loadWorkspaces={loadWorkspaces}
        navigate={path => navigate(path)}
        availableProviders={agentOps.availableProviders()}
        onRefreshProviders={agentOps.loadAvailableProviders}
      />

      <Show when={deregisterTarget()}>
        {target => (
          <WorkerSettingsDialog
            worker={target()}
            onClose={() => setDeregisterTarget(null)}
            onDeregistered={() => {
              setWorkers(prev => prev.filter(w => w.id !== target().id))
              setDeregisterTarget(null)
            }}
          />
        )}
      </Show>

      <Show when={addTunnelTarget()}>
        {target => (
          <AddTunnelDialog
            workerId={target().id}
            hubURL={window.location.origin}
            userId={auth.user()?.id ?? ''}
            onClose={() => setAddTunnelTarget(null)}
            onCreated={() => setAddTunnelTarget(null)}
          />
        )}
      </Show>
    </TunnelProvider>
  )
}
