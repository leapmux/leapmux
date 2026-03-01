import type { ParentComponent } from 'solid-js'
import type { SidebarElementsOpts } from './SidebarElements'
import { useLocation, useNavigate, useParams, useSearchParams } from '@solidjs/router'
import { createEffect, createMemo, createSignal, Show } from 'solid-js'
import { agentLoadingTimeoutMs, apiCallTimeout } from '~/api/transport'
import { NotFoundPage } from '~/components/common/NotFoundPage'
import { isWorkspaceMutatable } from '~/components/shell/sectionUtils'
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
import { createLayoutStore } from '~/stores/layout.store'
import { createSectionStore } from '~/stores/section.store'
import { createTabStore } from '~/stores/tab.store'
import { createTerminalStore } from '~/stores/terminal.store'
import { createWorkspaceStore } from '~/stores/workspace.store'
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
  const agentStore = createAgentStore()
  const chatStore = createChatStore()
  const terminalStore = createTerminalStore()
  const tabStore = createTabStore()
  const controlStore = createControlStore()
  const agentSessionStore = createAgentSessionStore()
  const layoutStore = createLayoutStore()
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

  // Debounced turn-end sound playback
  const TURN_END_SOUND_COOLDOWN_MS = 10_000
  let lastSoundPlayedAt = 0
  const playTurnEndSound = () => {
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
    settingsLoading,
    getOrgId: () => org.orgId(),
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    getTileActiveTabKeys: () => {
      const tileIds = layoutStore.getAllTileIds()
      return tileIds
        .map(id => tabStore.getActiveTabKeyForTile(id))
        .filter((key): key is string => key !== null)
    },
    onTurnEndSound: playTurnEndSound,
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
      return { workerId: agent?.workerId ?? '', workingDir: agent?.workingDir ?? '', homeDir: agent?.homeDir ?? '' }
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
  const { persistLayout } = useTabPersistence({
    tabStore,
    terminalStore,
    layoutStore,
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
    apiCallTimeout,
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
    persistLayout,
    focusEditor: () => focusEditorRef.current?.(),
    getScrollState: () => getScrollStateRef.current?.(),
    setFileTreePath,
    pendingWorktreeChoiceRef,
  })

  // Workspace restore (load agents/terminals/tabs/layout on workspace change)
  useWorkspaceRestore({
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    getOrgId: () => org.orgId(),
    agentStore,
    terminalStore,
    tabStore,
    layoutStore,
    setWorkspaceLoading,
  })

  // Tile drag-and-drop
  const tileDrag = useTileDragDrop({ tabStore, layoutStore, persistLayout })

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
  })

  // Sidebar element factories
  // Use getters for reactive values so that LeftSidebar/RightSidebar props
  // remain reactive when accessed through the intermediate opts object.
  const sidebarOpts = (): SidebarElementsOpts => ({
    get workspaces() { return workspaceStore.state.workspaces },
    get activeWorkspaceId() { return workspace.activeWorkspaceId() },
    sectionStore,
    tabStore,
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
  })

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
        <div style={{ '--mono-font-family': preferences.monoFontFamily(), 'display': 'contents' }}>
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
    </>
  )
}
