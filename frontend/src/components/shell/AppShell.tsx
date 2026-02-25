import type { Component, ParentComponent } from 'solid-js'
import type { CheckWorktreeStatusResponse } from '~/generated/leapmux/v1/git_pb'
import type { Tab } from '~/stores/tab.store'
import type { PermissionMode } from '~/utils/controlResponse'
import Resizable from '@corvu/resizable'
import { useLocation, useNavigate, useParams, useSearchParams } from '@solidjs/router'
import { createEffect, createMemo, createSignal, on, onMount, Show } from 'solid-js'
import { agentClient, gitClient, sectionClient, terminalClient, workspaceClient } from '~/api/clients'
import { agentCallTimeout, agentLoadingTimeoutMs, apiCallTimeout } from '~/api/transport'
import { AgentEditorPanel } from '~/components/chat/AgentEditorPanel'
import { ChatView } from '~/components/chat/ChatView'
import { ConfirmButton } from '~/components/common/ConfirmButton'
import { NotFoundPage } from '~/components/common/NotFoundPage'
import { showToast } from '~/components/common/Toast'
import { CrossTileDragProvider } from '~/components/shell/CrossTileDragContext'
import { LeftSidebar } from '~/components/shell/LeftSidebar'
import { NewAgentDialog } from '~/components/shell/NewAgentDialog'
import { NewTerminalDialog } from '~/components/shell/NewTerminalDialog'
import { ResumeSessionDialog } from '~/components/shell/ResumeSessionDialog'
import { RightSidebar } from '~/components/shell/RightSidebar'
import { TabBar } from '~/components/shell/TabBar'
import { getTerminalInstance, TerminalView } from '~/components/terminal/TerminalView'
import { NewWorkspaceDialog } from '~/components/workspace/NewWorkspaceDialog'
import { useAuth } from '~/context/AuthContext'
import { useOrg } from '~/context/OrgContext'
import { usePreferences } from '~/context/PreferencesContext'
import { useWorkspace } from '~/context/WorkspaceContext'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { useIsMobile } from '~/hooks/useIsMobile'
import { useWorkspaceConnection } from '~/hooks/useWorkspaceConnection'
import { after, mid } from '~/lib/lexorank'
import { createAgentStore } from '~/stores/agent.store'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabStore, tabKey } from '~/stores/tab.store'
import { createTerminalStore } from '~/stores/terminal.store'
import { createWorkspaceStore } from '~/stores/workspace.store'
import { dialogCompact } from '~/styles/shared.css'
import { isAgentWorking } from '~/utils/agentState'
import { buildInterruptRequest, buildSetPermissionModeRequest, DEFAULT_EFFORT, DEFAULT_MODEL } from '~/utils/controlResponse'
import * as styles from './AppShell.css'
import { Tile } from './Tile'
import { TilingLayout } from './TilingLayout'

/** Find the smallest unused number for auto-naming tabs (gap-filling). */
function nextTabNumber(tabs: Tab[], type: TabType, prefix: string): number {
  const used = new Set<number>()
  for (const tab of tabs) {
    if (tab.type === type && tab.title) {
      const match = tab.title.match(new RegExp(`^${prefix} (\\d+)$`))
      if (match)
        used.add(Number(match[1]))
    }
  }
  let n = 1
  while (used.has(n))
    n++
  return n
}

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
  const [workspaceLoading, setWorkspaceLoading] = createSignal(false)
  const [availableShells, setAvailableShells] = createSignal<string[]>([])
  const [defaultShell, setDefaultShell] = createSignal('')
  const [showResumeDialog, setShowResumeDialog] = createSignal(false)
  const [showNewAgentDialog, setShowNewAgentDialog] = createSignal(false)
  const [showNewTerminalDialog, setShowNewTerminalDialog] = createSignal(false)
  const [newAgentLoading, setNewAgentLoading] = createSignal(false)
  const [newTerminalLoading, setNewTerminalLoading] = createSignal(false)
  const [newShellLoading, setNewShellLoading] = createSignal(false)
  const [closingTabKeys, setClosingTabKeys] = createSignal<Set<string>>(new Set())
  // Pre-close worktree confirmation dialog state.
  // When set, shows a dialog asking the user what to do with a dirty worktree
  // BEFORE the tab is closed. The resolve callback communicates the user's choice.
  const [worktreeConfirm, setWorktreeConfirm] = createSignal<{
    path: string
    id: string
    branchName: string
    resolve: (choice: 'cancel' | 'keep' | 'remove') => void
  } | null>(null)
  // Stores the user's worktree choice so close handlers can auto-resolve cleanup.
  let pendingWorktreeChoice: 'keep' | 'remove' | null = null
  const settingsLoading = createLoadingSignal(agentLoadingTimeoutMs(true))

  // Mobile layout state
  const isMobile = useIsMobile()
  const [leftSidebarOpen, setLeftSidebarOpen] = createSignal(false)
  const [rightSidebarOpen, setRightSidebarOpen] = createSignal(false)
  const toggleLeftSidebar = () => setLeftSidebarOpen(prev => !prev)
  const toggleRightSidebar = () => setRightSidebarOpen(prev => !prev)
  const closeAllSidebars = () => {
    setLeftSidebarOpen(false)
    setRightSidebarOpen(false)
  }

  const addClosingTabKey = (key: string) =>
    setClosingTabKeys(prev => new Set([...prev, key]))
  const removeClosingTabKey = (key: string) =>
    setClosingTabKeys((prev) => {
      const next = new Set(prev)
      next.delete(key)
      return next
    })

  // Debounced turn-end sound playback (cooldown prevents rapid repeated sounds).
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

  // Streaming connection management (watchEvents, workerOnline)

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

  // Auto-open new workspace dialog from URL search params (e.g. after worker registration)
  createEffect(() => {
    if (searchParams.newWorkspace === 'true') {
      // Save the worker ID before clearing search params
      setPreselectedWorkerId(searchParams.workerId as string | undefined)
      setShowNewWorkspace(true)
      // Clear the search params so it doesn't re-trigger
      setSearchParams({ newWorkspace: undefined, workerId: undefined }, { replace: true })
    }
  })

  // Detect if we're on a workspace route
  const isWorkspaceRoute = createMemo(() => {
    const path = location.pathname
    const orgPrefix = `/o/${params.orgSlug}`
    return path === orgPrefix || path === `${orgPrefix}/` || path.startsWith(`${orgPrefix}/workspace/`)
  })

  // True when the URL has a workspace ID but it doesn't exist in the loaded list.
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

  // Load workspaces on mount and when org changes
  const loadWorkspaces = async () => {
    const orgId = org.orgId()
    if (!orgId)
      return
    workspaceStore.setLoading(true)
    try {
      const resp = await workspaceClient.listWorkspaces({ orgId })
      workspaceStore.setWorkspaces(resp.workspaces)
    }
    catch (err) {
      workspaceStore.setError(String(err))
    }
    finally {
      workspaceStore.setLoading(false)
    }
  }

  createEffect(() => {
    if (org.orgId()) {
      loadWorkspaces()
    }
  })

  // Auto-activate workspace when navigating to org root with no workspace selected.
  // Use sessionStorage-persisted active workspace, falling back to first workspace.
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

  // Dynamic page title (only on workspace-related routes; other routes set their own titles)
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

  // Debounced layout + tab persistence
  let layoutSaveTimer: ReturnType<typeof setTimeout> | null = null
  const persistLayout = () => {
    const ws = activeWorkspace()
    if (!ws || workspaceLoading())
      return
    if (layoutSaveTimer)
      clearTimeout(layoutSaveTimer)
    layoutSaveTimer = setTimeout(() => {
      const tileIds = layoutStore.getAllTileIds()
      const activeTabs = tileIds.map((tileId) => {
        const activeKey = tabStore.getActiveTabKeyForTile(tileId)
        if (!activeKey)
          return null
        const parts = activeKey.split(':')
        return { tileId, tabType: Number(parts[0]) as TabType, tabId: parts[1] }
      }).filter(Boolean) as Array<{ tileId: string, tabType: TabType, tabId: string }>

      const tabs = tabStore.state.tabs.map(t => ({
        tabType: t.type,
        tabId: t.id,
        position: t.position ?? '',
        tileId: t.tileId ?? '',
      }))

      workspaceClient.saveLayout({
        orgId: org.orgId(),
        workspaceId: ws.id,
        layout: layoutStore.toProto(),
        activeTabs,
        tabs,
      }).catch(() => {})
    }, 500)
  }

  // Whether the current user owns the active workspace
  const isWorkspaceOwner = createMemo(() => {
    const ws = activeWorkspace()
    if (!ws)
      return false
    return ws.createdBy === auth.user()?.id
  })

  // Active tab derived state
  const activeTab = createMemo(() => tabStore.activeTab())
  const activeTabType = createMemo(() => activeTab()?.type ?? null)

  // Get worker and working directory from the currently active tab.
  // Agent tabs carry these from the proto; terminal tabs carry them from the store.
  const getCurrentTabContext = (): { workerId: string, workingDir: string } => {
    const tab = activeTab()
    if (!tab)
      return { workerId: '', workingDir: '' }
    if (tab.type === TabType.AGENT) {
      const agent = agentStore.state.agents.find(a => a.id === tab.id)
      return { workerId: agent?.workerId ?? '', workingDir: agent?.workingDir ?? '' }
    }
    else {
      const terminal = terminalStore.state.terminals.find(t => t.id === tab.id)
      return { workerId: terminal?.workerId ?? '', workingDir: terminal?.workingDir ?? '' }
    }
  }

  // Load available shells when active tab's worker changes
  createEffect(() => {
    const ws = activeWorkspace()
    if (!ws)
      return
    const ctx = getCurrentTabContext()
    if (!ctx.workerId) {
      setAvailableShells([])
      setDefaultShell('')
      return
    }
    terminalClient.listAvailableShells({ orgId: org.orgId(), workspaceId: ws.id, workerId: ctx.workerId })
      .then((resp) => {
        setAvailableShells(resp.shells)
        setDefaultShell(resp.defaultShell)
      })
      .catch(() => {
        setAvailableShells([])
        setDefaultShell('')
      })
  })

  // Focus callback for the markdown editor (shared editor panel)
  let focusEditor: (() => void) | undefined

  // Ref for retrieving the first visible message seq from ChatView (for viewport save on tab switch).
  let getScrollState: (() => { distFromBottom: number, atBottom: boolean } | undefined) | undefined

  // Container height for the center panel (used for max editor height calculation)
  const [centerPanelHeight, setCenterPanelHeight] = createSignal(0)

  // Open a new agent in the given workspace
  const openAgentInWorkspace = async (workspaceId: string, workerId: string, workingDir: string, sessionId?: string) => {
    try {
      const title = `Agent ${nextTabNumber(tabStore.state.tabs, TabType.AGENT, 'Agent')}`
      const resp = await agentClient.openAgent({
        workspaceId,
        model: DEFAULT_MODEL,
        title,
        systemPrompt: '',
        workerId,
        workingDir,
        ...(sessionId ? { agentSessionId: sessionId } : {}),
      }, agentCallTimeout(false))
      if (resp.agent) {
        const tileId = layoutStore.focusedTileId()
        agentStore.addAgent(resp.agent)
        tabStore.addTab({
          type: TabType.AGENT,
          id: resp.agent.id,
          title,
          tileId,
          workerId: resp.agent.workerId,
          workingDir: resp.agent.workingDir,
        })
        tabStore.setActiveTabForTile(tileId, TabType.AGENT, resp.agent.id)
        persistLayout()
        // Focus the editor after the reactive updates propagate to the DOM.
        requestAnimationFrame(() => focusEditor?.())
      }
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to open agent', 'danger')
    }
  }

  // Open a new agent in the active workspace (for click handlers)
  const handleOpenAgent = async () => {
    const ws = activeWorkspace()
    if (!ws)
      return
    const ctx = getCurrentTabContext()
    if (!ctx.workerId) {
      setShowNewAgentDialog(true)
      return
    }
    setNewAgentLoading(true)
    try {
      await openAgentInWorkspace(ws.id, ctx.workerId, ctx.workingDir)
    }
    finally {
      setNewAgentLoading(false)
    }
  }

  // Open a terminal with a specific shell
  const handleOpenTerminalWithShell = async (shell: string) => {
    const ws = activeWorkspace()
    if (!ws)
      return
    const ctx = getCurrentTabContext()
    if (!ctx.workerId) {
      setShowNewTerminalDialog(true)
      return
    }
    setNewShellLoading(true)
    try {
      const title = `Terminal ${nextTabNumber(tabStore.state.tabs, TabType.TERMINAL, 'Terminal')}`
      const resp = await terminalClient.openTerminal({
        orgId: org.orgId(),
        workspaceId: ws.id,
        cols: 80,
        rows: 24,
        workingDir: ctx.workingDir,
        shell,
        workerId: ctx.workerId,
      })

      const tileId = layoutStore.focusedTileId()
      terminalStore.addTerminal({ id: resp.terminalId, workspaceId: ws.id, workerId: ctx.workerId, workingDir: ctx.workingDir })
      tabStore.addTab({ type: TabType.TERMINAL, id: resp.terminalId, title, tileId, workerId: ctx.workerId, workingDir: ctx.workingDir })
      tabStore.setActiveTabForTile(tileId, TabType.TERMINAL, resp.terminalId)
      persistLayout()
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to open terminal', 'danger')
    }
    finally {
      setNewShellLoading(false)
    }
  }

  // Resume an agent from an existing session ID
  const handleResumeAgent = async (sessionId: string, workerId: string) => {
    const ws = activeWorkspace()
    if (!ws)
      return
    try {
      const ctx = getCurrentTabContext()
      await openAgentInWorkspace(ws.id, workerId, ctx.workingDir || '~', sessionId)
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to resume session', 'danger')
    }
    finally {
      setShowResumeDialog(false)
    }
  }

  // Load agents and set up watchers when active workspace changes.
  // Use `on()` to explicitly track only `activeWorkspaceId` and `orgId` —
  // without it, SolidJS could track other reactive reads in the effect body
  // and re-run the effect spuriously, creating duplicate async chains.
  // Both are tracked because after page reload, orgId resolves asynchronously
  // and must be available before API calls that require it (listTabs, getLayout).
  let loadGeneration = 0
  createEffect(on([workspace.activeWorkspaceId, org.orgId], ([activeId, currentOrgId]) => {
    if (!activeId || !currentOrgId)
      return

    // Bump generation so stale Promise.all callbacks are discarded.
    const gen = ++loadGeneration

    // Clear tabs from previous workspace
    setWorkspaceLoading(true)
    tabStore.clear()

    // Load agents for this workspace.
    // Guard async callbacks: if the user navigated away before the response
    // arrives, the workspace ID will have changed and we must discard the result.
    const agentsLoaded = agentClient.listAgents({ workspaceId: activeId })
      .then((resp) => {
        if (gen !== loadGeneration)
          return
        agentStore.setAgents(resp.agents)
      })
      .catch(() => {})

    // Restore terminals from server
    const terminalsLoaded = terminalClient.listTerminals({ orgId: currentOrgId, workspaceId: activeId })
      .then((resp) => {
        if (gen !== loadGeneration)
          return
        // Clear existing terminals for this workspace
        terminalStore.setTerminals([])
        for (const t of resp.terminals) {
          terminalStore.addTerminal({
            id: t.terminalId,
            workspaceId: activeId,
            screen: t.screen.length > 0 ? t.screen : undefined,
            cols: t.cols || undefined,
            rows: t.rows || undefined,
          })
          if (t.exited) {
            terminalStore.markExited(t.terminalId)
          }
        }
      })
      .catch(() => {})

    // Load persisted tab ordering
    const tabsLoaded = workspaceClient.listTabs({ orgId: currentOrgId, workspaceId: activeId })
      .catch(() => null)

    // Load tiling layout
    const layoutLoaded = workspaceClient.getLayout({ orgId: currentOrgId, workspaceId: activeId })
      .catch(() => null)

    // After everything loads, create tabs, apply ordering, and restore active tab.
    // Tabs are created here (not in individual handlers) so we can use the
    // persisted workspace_tabs to determine which closed agents/terminals
    // should have tabs (user-closed tabs are removed from workspace_tabs).
    Promise.all([agentsLoaded, terminalsLoaded, tabsLoaded, layoutLoaded]).then(([, , tabsResp, layoutResp]) => {
      // Discard stale callbacks from previous loads or duplicate effect runs.
      if (gen !== loadGeneration)
        return

      // Clear tabs to ensure idempotency if this callback runs more than
      // once (e.g. due to the effect re-running for the same workspace).
      tabStore.clear()

      // Restore tiling layout
      if (layoutResp?.layout) {
        layoutStore.fromProto(layoutResp.layout)
      }
      else {
        layoutStore.initSingleTile()
      }

      // Build map of persisted tab keys -> tileIds
      const persistedKeys = new Set<string>()
      const tabTileMap = new Map<string, string>()
      if (tabsResp?.tabs) {
        for (const t of tabsResp.tabs) {
          const key = `${t.tabType}:${t.tabId}`
          persistedKeys.add(key)
          if (t.tileId) {
            tabTileMap.set(key, t.tileId)
          }
        }
      }

      // Get the list of valid tile IDs from the layout
      const validTileIds = new Set(layoutStore.getAllTileIds())
      const defaultTileId = layoutStore.focusedTileId()

      // Create agent tabs: always for ACTIVE agents, otherwise only if
      // the tab is in workspace_tabs (i.e. the user hasn't closed it).
      for (const a of agentStore.state.agents) {
        if (a.status === AgentStatus.ACTIVE || persistedKeys.has(`${TabType.AGENT}:${a.id}`)) {
          const key = `${TabType.AGENT}:${a.id}`
          let tileId = tabTileMap.get(key) ?? defaultTileId
          if (!validTileIds.has(tileId))
            tileId = defaultTileId
          tabStore.addTab({ type: TabType.AGENT, id: a.id, title: a.title || undefined, tileId, workerId: a.workerId, workingDir: a.workingDir }, false)
        }
      }

      // Create terminal tabs: always for non-exited terminals, otherwise
      // only if the tab is in workspace_tabs.
      for (const t of terminalStore.state.terminals) {
        if (!terminalStore.isExited(t.id) || persistedKeys.has(`${TabType.TERMINAL}:${t.id}`)) {
          const key = `${TabType.TERMINAL}:${t.id}`
          let tileId = tabTileMap.get(key) ?? defaultTileId
          if (!validTileIds.has(tileId))
            tileId = defaultTileId
          tabStore.addTab({ type: TabType.TERMINAL, id: t.id, tileId }, false)
        }
      }

      // Apply persisted tab positions
      if (tabsResp && tabsResp.tabs.length > 0) {
        const posMap = new Map<string, string>()
        for (const t of tabsResp.tabs) {
          posMap.set(`${t.tabType}:${t.tabId}`, t.position)
        }
        tabStore.sortByPositions(posMap)
      }

      // Restore per-tile active tabs from layout response
      if (layoutResp?.activeTabs && layoutResp.activeTabs.length > 0) {
        for (const at of layoutResp.activeTabs) {
          tabStore.setActiveTabForTile(at.tileId, at.tabType, at.tabId)
        }
      }

      // Restore active tab from sessionStorage (per-browser-tab state)
      const savedKey = sessionStorage.getItem(`leapmux:activeTab:${activeId}`)
      if (savedKey && tabStore.state.tabs.some(t => tabKey(t) === savedKey)) {
        const parts = savedKey.split(':')
        const tabType = Number(parts[0]) as TabType
        const tabId = parts[1]
        tabStore.setActiveTab(tabType, tabId)
        // Also set per-tile active tab so the tile renders the tab content
        const restoredTab = tabStore.state.tabs.find(t => tabKey(t) === savedKey)
        if (restoredTab?.tileId) {
          tabStore.setActiveTabForTile(restoredTab.tileId, tabType, tabId)
        }
        if (tabType === TabType.AGENT) {
          agentStore.setActiveAgent(tabId)
        }
        else {
          terminalStore.setActiveTerminal(tabId)
        }
      }
      else if (tabStore.state.tabs.length > 0) {
        // Activate first tab if no saved state
        const firstTab = tabStore.state.tabs[0]
        tabStore.setActiveTab(firstTab.type, firstTab.id)
        // Also set per-tile active tab so the tile renders the tab content
        if (firstTab.tileId) {
          tabStore.setActiveTabForTile(firstTab.tileId, firstTab.type, firstTab.id)
        }
        if (firstTab.type === TabType.AGENT) {
          agentStore.setActiveAgent(firstTab.id)
        }
      }

      setWorkspaceLoading(false)
    })
  }))

  // Persist active tab to sessionStorage (per-browser-tab state).
  // Skip while loading to prevent intermediate state from overwriting.
  createEffect(() => {
    const activeKey = tabStore.state.activeTabKey
    const wsId = workspace.activeWorkspaceId()
    if (wsId && activeKey && !workspaceLoading()) {
      sessionStorage.setItem(`leapmux:activeTab:${wsId}`, activeKey)
    }
  })

  // Persist active workspace to sessionStorage (per-browser-tab state).
  createEffect(() => {
    const wsId = workspace.activeWorkspaceId()
    if (wsId && !workspaceLoading()) {
      sessionStorage.setItem('leapmux:activeWorkspace', wsId)
    }
  })

  // Active agent todos (for right sidebar To-dos pane)
  const activeTodos = createMemo(() => {
    const id = agentStore.state.activeAgentId
    if (!id)
      return []
    return chatStore.getTodos(id)
  })

  const showTodos = createMemo(() => activeTabType() === TabType.AGENT && activeTodos().length > 0)

  // Handle control responses (permission grant/deny) for agent prompts
  const handleControlResponse = async (agentId: string, content: Uint8Array) => {
    try {
      const agent = agentStore.state.agents.find(a => a.id === agentId)
      const isActive = agent?.status === AgentStatus.ACTIVE
      await agentClient.sendControlResponse({ agentId, content }, agentCallTimeout(isActive))
      // Remove from pending after successful send.
      const parsed = JSON.parse(new TextDecoder().decode(content))
      const requestId = parsed?.response?.request_id
      if (requestId) {
        controlStore.removeRequest(agentId, requestId)
      }
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to send response', 'danger')
    }
  }

  // Change model or effort for the active agent (requires agent restart)
  const handleModelOrEffortChange = async (field: 'model' | 'effort', value: string) => {
    const agentId = agentStore.state.activeAgentId
    if (!agentId)
      return
    const agent = agentStore.state.agents.find(a => a.id === agentId)
    if (!agent)
      return
    const previous = agent[field] || (field === 'model' ? DEFAULT_MODEL : DEFAULT_EFFORT)
    // Optimistic update
    agentStore.updateAgent(agentId, { [field]: value })
    settingsLoading.start()
    try {
      await agentClient.updateAgentSettings({
        agentId,
        model: field === 'model' ? value : '',
        effort: field === 'effort' ? value : '',
      }, agentCallTimeout(agent.status === AgentStatus.ACTIVE))
      settingsLoading.stop()
    }
    catch (err) {
      agentStore.updateAgent(agentId, { [field]: previous })
      settingsLoading.stop()
      showToast(err instanceof Error ? err.message : `Failed to change ${field}`, 'danger')
    }
  }

  // Interrupt the active agent's current turn
  const handleInterrupt = async () => {
    const agentId = agentStore.state.activeAgentId
    if (!agentId)
      return
    try {
      await agentClient.sendAgentMessage({
        agentId,
        content: buildInterruptRequest(),
      }, agentCallTimeout(true))
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to interrupt', 'danger')
    }
  }

  // Change permission mode for the active agent
  const handlePermissionModeChange = async (mode: PermissionMode) => {
    const agentId = agentStore.state.activeAgentId
    if (!agentId)
      return
    const agent = agentStore.state.agents.find(a => a.id === agentId)
    if (!agent)
      return
    const previousMode = (agent.permissionMode || 'default') as PermissionMode
    // Optimistic update
    agentStore.updateAgent(agentId, { permissionMode: mode })
    settingsLoading.start()
    try {
      await agentClient.sendAgentMessage({
        agentId,
        content: buildSetPermissionModeRequest(mode),
      }, agentCallTimeout(agent.status === AgentStatus.ACTIVE))
      settingsLoading.stop()
    }
    catch (err) {
      // Revert on failure
      agentStore.updateAgent(agentId, { permissionMode: previousMode })
      settingsLoading.stop()
      showToast(err instanceof Error ? err.message : 'Failed to change permission mode', 'danger')
    }
  }

  // Retry a failed message delivery
  const handleRetryMessage = async (agentId: string, messageId: string) => {
    try {
      const retryAgent = agentStore.state.agents.find(a => a.id === agentId)
      await agentClient.retryAgentMessage({ agentId, messageId }, agentCallTimeout(retryAgent?.status === AgentStatus.ACTIVE))
      chatStore.clearMessageError(messageId)
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Retry failed', 'danger')
    }
  }

  // Delete a failed message
  const handleDeleteMessage = async (agentId: string, messageId: string) => {
    try {
      await agentClient.deleteAgentMessage({ agentId, messageId })
      chatStore.removeMessage(agentId, messageId)
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to delete message', 'danger')
    }
  }

  // Close an agent
  const handleCloseAgent = async (agentId: string) => {
    try {
      controlStore.clearAgent(agentId)
      const resp = await agentClient.closeAgent({ agentId })
      agentStore.removeAgent(agentId)
      tabStore.removeTab(TabType.AGENT, agentId)
      // Auto-handle worktree cleanup if the pre-close check stored a choice.
      if (resp.worktreeCleanupPending && resp.worktreeId) {
        if (pendingWorktreeChoice === 'remove') {
          gitClient.forceRemoveWorktree({ worktreeId: resp.worktreeId }, apiCallTimeout()).catch(() => {})
        }
        else {
          // Default to keep (if somehow no choice was stored)
          gitClient.keepWorktree({ worktreeId: resp.worktreeId }).catch(() => {})
        }
      }
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to close agent', 'danger')
    }
  }

  // Workspace selection navigates to URL
  const handleSelectWorkspace = (id: string) => {
    closeAllSidebars()
    navigate(`/o/${params.orgSlug}/workspace/${id}`)
  }

  // Handle workspace deletion — activate the next workspace or go to dashboard
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

  // Terminal handlers
  const handleTerminalInput = async (terminalId: string, data: Uint8Array) => {
    try {
      const ws = activeWorkspace()
      if (!ws || !terminalStore.hasTerminal(terminalId) || terminalStore.isExited(terminalId))
        return
      await terminalClient.sendInput({ orgId: org.orgId(), workspaceId: ws.id, terminalId, data })
    }
    catch {
      // ignore input errors
    }
  }

  const handleTerminalTitleChange = (terminalId: string, title: string) => {
    terminalStore.updateTerminalTitle(terminalId, title)
    tabStore.updateTabTitle(TabType.TERMINAL, terminalId, title)
  }

  const handleTerminalBell = (terminalId: string) => {
    // Only notify if this terminal's tab is not active
    const activeKey = tabStore.state.activeTabKey
    const bellKey = `terminal:${terminalId}`
    if (activeKey !== bellKey) {
      tabStore.setNotification(TabType.TERMINAL, terminalId, true)
    }
  }

  const handleTerminalResize = async (terminalId: string, cols: number, rows: number) => {
    try {
      const ws = activeWorkspace()
      if (!ws || !terminalStore.hasTerminal(terminalId) || terminalStore.isExited(terminalId))
        return
      await terminalClient.resizeTerminal({ orgId: org.orgId(), workspaceId: ws.id, terminalId, cols, rows })
    }
    catch {
      // ignore resize errors
    }
  }

  const handleTerminalClose = async (terminalId: string) => {
    try {
      const ws = activeWorkspace()
      if (!ws)
        return
      const resp = await terminalClient.closeTerminal({ orgId: org.orgId(), workspaceId: ws.id, terminalId })
      // Auto-handle worktree cleanup if the pre-close check stored a choice.
      if (resp.worktreeCleanupPending && resp.worktreeId) {
        if (pendingWorktreeChoice === 'remove') {
          gitClient.forceRemoveWorktree({ worktreeId: resp.worktreeId }, apiCallTimeout()).catch(() => {})
        }
        else {
          gitClient.keepWorktree({ worktreeId: resp.worktreeId }).catch(() => {})
        }
      }
    }
    catch {
      // Ignore errors (e.g. terminal already exited or not tracked by worker)
    }
    finally {
      terminalStore.removeTerminal(terminalId)
      tabStore.removeTab(TabType.TERMINAL, terminalId)
    }
  }

  const handleOpenTerminal = async () => {
    const ws = activeWorkspace()
    if (!ws)
      return
    const ctx = getCurrentTabContext()
    if (!ctx.workerId) {
      setShowNewTerminalDialog(true)
      return
    }
    setNewTerminalLoading(true)
    try {
      const title = `Terminal ${nextTabNumber(tabStore.state.tabs, TabType.TERMINAL, 'Terminal')}`
      const resp = await terminalClient.openTerminal({
        orgId: org.orgId(),
        workspaceId: ws.id,
        cols: 80,
        rows: 24,
        workingDir: ctx.workingDir,
        shell: '',
        workerId: ctx.workerId,
      })

      const tileId = layoutStore.focusedTileId()
      terminalStore.addTerminal({ id: resp.terminalId, workspaceId: ws.id, workerId: ctx.workerId, workingDir: ctx.workingDir })
      tabStore.addTab({ type: TabType.TERMINAL, id: resp.terminalId, title, tileId, workerId: ctx.workerId, workingDir: ctx.workingDir })
      tabStore.setActiveTabForTile(tileId, TabType.TERMINAL, resp.terminalId)
      persistLayout()
    }
    catch (err) {
      showToast(err instanceof Error ? err.message : 'Failed to open terminal', 'danger')
    }
    finally {
      setNewTerminalLoading(false)
    }
  }

  // Tracks whether a tab is being renamed (to avoid stealing focus)
  let isTabEditing: () => boolean = () => false

  // Handle tab selection from the tab bar
  const handleTabSelect = (tab: Tab) => {
    // Save viewport scroll state before switching away from an agent tab.
    const prevAgentId = agentStore.state.activeAgentId
    if (prevAgentId) {
      const scrollState = getScrollState?.()
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
        focusEditor?.()
      })
    }
    else {
      terminalStore.setActiveTerminal(tab.id)
      requestAnimationFrame(() => {
        if (isTabEditing())
          return
        const instance = getTerminalInstance(tab.id)
        instance?.terminal.focus()
      })
    }
  }

  // Show the worktree confirmation dialog and wait for the user's choice.
  // Returns 'cancel', 'keep', or 'remove'.
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

  // Handle tab close from the tab bar
  const handleTabClose = async (tab: Tab) => {
    // Pre-close check: does this tab have a dirty worktree?
    try {
      const tabType = tab.type === TabType.AGENT ? TabType.AGENT : TabType.TERMINAL
      const status = await gitClient.checkWorktreeStatus({ tabType, tabId: tab.id }, apiCallTimeout())
      if (status.hasWorktree && status.isLastTab && status.isDirty) {
        const choice = await askWorktreeConfirmation(status)
        if (choice === 'cancel') {
          return // User cancelled — don't close the tab
        }
        pendingWorktreeChoice = choice
      }
    }
    catch {
      // If the pre-check fails, proceed with close (best-effort).
    }

    const key = tabKey(tab)
    addClosingTabKey(key)
    try {
      if (tab.type === TabType.AGENT) {
        await handleCloseAgent(tab.id)
      }
      else {
        // Clean up the terminal instance
        const instance = getTerminalInstance(tab.id)
        if (instance) {
          instance.dispose()
        }
        await handleTerminalClose(tab.id)
      }
    }
    finally {
      removeClosingTabKey(key)
      pendingWorktreeChoice = null
    }
  }

  // Reset file tree selection when active tab changes
  createEffect(() => {
    // Track active tab to trigger on tab change
    const _tab = activeTab()
    const ctx = getCurrentTabContext()
    setFileTreePath(ctx.workingDir || '~')
  })

  // --- Shared sub-components rendered in both desktop and mobile layouts ---

  const hasMultipleTiles = createMemo(() => layoutStore.getAllTileIds().length > 1)

  // Handle intra-tile tab reorder (from CrossTileDragProvider)
  const handleIntraTileReorder = (tileId: string, fromKey: string, toKey: string) => {
    tabStore.reorderTabs(fromKey, toKey)
    persistLayout()
  }

  // Handle cross-tile tab move (from CrossTileDragProvider)
  const handleCrossTileMove = (fromTileId: string, toTileId: string, draggedTabKey: string, nearTabKey: string | null) => {
    // Move the tab to the target tile
    tabStore.moveTabToTile(draggedTabKey, toTileId)

    // Calculate new LexoRank position
    const targetTabs = tabStore.getTabsForTile(toTileId)
    let newPosition: string
    if (nearTabKey) {
      // Insert near the specified tab
      const nearIdx = targetTabs.findIndex(t => tabKey(t) === nearTabKey)
      if (nearIdx >= 0) {
        const prevPos = nearIdx > 0 ? targetTabs[nearIdx - 1]?.position ?? '' : ''
        const nextPos = targetTabs[nearIdx]?.position ?? ''
        newPosition = mid(prevPos, nextPos)
      }
      else {
        // Fallback: append at end
        const lastTab = targetTabs[targetTabs.length - 1]
        newPosition = lastTab?.position ? after(lastTab.position) : 'a'
      }
    }
    else {
      // Append at end
      const lastTab = targetTabs[targetTabs.length - 1]
      newPosition = lastTab?.position ? after(lastTab.position) : 'a'
    }
    tabStore.setTabPosition(draggedTabKey, newPosition)

    // Activate the moved tab in the target tile
    const parts = draggedTabKey.split(':')
    if (parts.length === 2) {
      tabStore.setActiveTabForTile(toTileId, Number(parts[0]) as TabType, parts[1])
    }

    persistLayout()
  }

  // Look up which tile a tab belongs to (for CrossTileDragProvider)
  const lookupTileIdForTab = (key: string): string | undefined => {
    const tab = tabStore.state.tabs.find(t => tabKey(t) === key)
    return tab?.tileId
  }

  // Render drag overlay for a tab (for CrossTileDragProvider)
  const renderDragOverlay = (key: string) => {
    const tab = tabStore.state.tabs.find(t => tabKey(t) === key)
    if (!tab)
      return <></>
    const label = tab.title || (tab.type === TabType.AGENT ? 'Agent' : 'Terminal')
    // Import inline to avoid circular deps — using the CSS class from TabBar.css
    return (
      <div class={styles.dragPreviewTooltip}>
        <span>{label}</span>
      </div>
    )
  }

  const createTabBarForTile = (tileId: string) => (
    <TabBar
      tileId={tileId}
      tabs={tabStore.getTabsForTile(tileId)}
      activeTabKey={tabStore.getActiveTabKeyForTile(tileId)}
      showAddButton={isWorkspaceRoute() && !!activeWorkspace() && isWorkspaceOwner()}
      onSelect={(tab) => {
        layoutStore.setFocusedTile(tileId)
        handleTabSelect(tab)
        tabStore.setActiveTabForTile(tileId, tab.type, tab.id)
      }}
      onClose={handleTabClose}
      onRename={(tab, title) => {
        tabStore.updateTabTitle(tab.type, tab.id, title)
        if (tab.type === TabType.AGENT) {
          agentClient.renameAgent({ agentId: tab.id, title }).catch((err) => {
            showToast(err instanceof Error ? err.message : 'Failed to rename agent', 'danger')
          })
        }
      }}
      hasActiveTabContext={!!getCurrentTabContext().workerId}
      isEditingRef={(fn) => { isTabEditing = fn }}
      onNewAgent={handleOpenAgent}
      onNewTerminal={handleOpenTerminal}
      availableShells={availableShells()}
      defaultShell={defaultShell()}
      onNewTerminalWithShell={handleOpenTerminalWithShell}
      onResumeSession={() => setShowResumeDialog(true)}
      onNewAgentAdvanced={() => setShowNewAgentDialog(true)}
      onNewTerminalAdvanced={() => setShowNewTerminalDialog(true)}
      newAgentLoading={newAgentLoading()}
      newTerminalLoading={newTerminalLoading()}
      newShellLoading={newShellLoading()}
      closingTabKeys={closingTabKeys()}
      isMobile={isMobile()}
      onToggleLeftSidebar={toggleLeftSidebar}
      onToggleRightSidebar={toggleRightSidebar}
      tileActions={{
        canSplit: layoutStore.canSplitTile(tileId),
        canClose: hasMultipleTiles(),
        onSplitHorizontal: () => {
          layoutStore.splitTileHorizontal(tileId)
          persistLayout()
        },
        onSplitVertical: () => {
          layoutStore.splitTileVertical(tileId)
          persistLayout()
        },
        onClose: () => {
          layoutStore.closeTile(tileId)
          persistLayout()
        },
      }}
    />
  )

  // Legacy single-tile tab bar (used on mobile)
  const tabBarElement = () => createTabBarForTile(layoutStore.focusedTileId())

  // Resolve the active tab for a specific tile (reactive)
  const getActiveTabForTile = (tileId: string): Tab | null => {
    const key = tabStore.getActiveTabKeyForTile(tileId)
    if (!key)
      return null
    return tabStore.state.tabs.find(t => tabKey(t) === key) ?? null
  }

  // Render the content for a specific tile based on its active tab.
  // Agent and terminal panes are rendered side-by-side so that xterm.js
  // instances survive tab switches; CSS hides the inactive pane.
  const renderTileContent = (tileId: string) => {
    const tab = () => getActiveTabForTile(tileId)
    const agentTab = () => {
      const t = tab()
      return t?.type === TabType.AGENT ? t : null
    }
    const terminalTab = () => {
      const t = tab()
      return t?.type === TabType.TERMINAL ? t : null
    }
    const tileTerminalIds = () => new Set(
      tabStore.getTabsForTile(tileId)
        .filter(t => t.type === TabType.TERMINAL)
        .map(t => t.id),
    )
    const hasTerminals = () => tileTerminalIds().size > 0
    // Only pass terminals that belong to this tile so that each tile's
    // TerminalView creates xterm.js instances exclusively for its own
    // terminals. Without this filter, every tile would mount containers
    // for ALL terminals and the duplicate `terminal.open(ref)` calls
    // would steal xterm.js instances from other tiles, leaving them blank.
    const tileTerminals = () => {
      const ids = tileTerminalIds()
      return terminalStore.state.terminals.filter(t => ids.has(t.id))
    }

    return (
      <>
        {/* Agent content — mounted/unmounted with the active agent tab */}
        <Show when={agentTab()} keyed>
          {(at) => {
            const agentId = at.id
            const agent = () => agentStore.state.agents.find(a => a.id === agentId)
            return (
              <div class={styles.centerContent}>
                <Show
                  when={agent()}
                  fallback={<div class={styles.placeholder}>Agent not found.</div>}
                >
                  <ChatView
                    messages={chatStore.getMessages(agentId)}
                    streamingText={chatStore.state.streamingText[agentId] ?? ''}
                    agentWorking={agentStore.state.agents.find(a => a.id === agentId)?.status === AgentStatus.ACTIVE && isAgentWorking(chatStore.getMessages(agentId)) && controlStore.getRequests(agentId).length === 0}
                    messageErrors={chatStore.state.messageErrors}
                    onRetryMessage={messageId => handleRetryMessage(agentId, messageId)}
                    onDeleteMessage={messageId => handleDeleteMessage(agentId, messageId)}
                    workingDir={agentStore.state.agents.find(a => a.id === agentId)?.workingDir}
                    homeDir={agentStore.state.agents.find(a => a.id === agentId)?.homeDir}
                    hasOlderMessages={chatStore.hasOlderMessages(agentId)}
                    fetchingOlder={chatStore.isFetchingOlder(agentId)}
                    onLoadOlderMessages={() => chatStore.loadOlderMessages(agentId)}
                    onTrimOldMessages={() => chatStore.trimOldMessages(agentId, 150)}
                    savedViewportScroll={chatStore.getSavedViewportScroll(agentId)}
                    onClearSavedViewportScroll={() => chatStore.clearSavedViewportScroll(agentId)}
                    scrollStateRef={(fn) => { getScrollState = fn }}
                  />
                </Show>
              </div>
            )
          }}
        </Show>

        {/* Terminal content — stays mounted while terminal tabs exist so
            that xterm.js instances are preserved across tab switches. */}
        <Show when={hasTerminals()}>
          <div
            class={styles.centerContent}
            classList={{ [styles.layoutHidden]: !terminalTab() }}
          >
            <TerminalView
              terminals={tileTerminals()}
              activeTerminalId={terminalTab()?.id ?? null}
              visible={!!terminalTab()}
              onInput={handleTerminalInput}
              onResize={handleTerminalResize}
              onTitleChange={handleTerminalTitleChange}
              onBell={handleTerminalBell}
            />
          </div>
        </Show>

        {/* Fallback when no tabs exist */}
        <Show when={!tab() && activeWorkspace()}>
          <div class={styles.placeholder}>
            No tabs in this tile. Click + to open an agent or terminal.
          </div>
        </Show>
      </>
    )
  }

  // Focused tile's active agent ID (stable string for keyed <Show>).
  const focusedAgentId = createMemo(() => {
    const tileId = layoutStore.focusedTileId()
    const tab = getActiveTabForTile(tileId)
    if (!tab || tab.type !== TabType.AGENT)
      return null
    return tab.id
  })

  // Renders the AgentEditorPanel for the focused agent. Reads
  // focusedAgentId() from the closure (not from a keyed <Show> accessor)
  // to avoid stale accessor warnings during unmount.
  const FocusedAgentEditorPanel: Component<{ containerHeight: number }> = (props) => {
    const agentId = () => focusedAgentId()!
    return (
      <AgentEditorPanel
        agentId={agentId()}
        agent={agentStore.state.agents.find(a => a.id === agentId())}
        // eslint-disable-next-line solid/reactivity -- event handler, not a tracked scope
        onSendMessage={async (content) => {
          const id = focusedAgentId()
          if (!id)
            return
          try {
            const sendAgent = agentStore.state.agents.find(a => a.id === id)
            await agentClient.sendAgentMessage({ agentId: id, content }, agentCallTimeout(sendAgent?.status === AgentStatus.ACTIVE))
          }
          catch (err) {
            showToast(err instanceof Error ? err.message : 'Failed to send message', 'danger')
          }
        }}
        disabled={false}
        focusRef={(fn) => { focusEditor = fn }}
        controlRequests={controlStore.getRequests(agentId())}
        onControlResponse={handleControlResponse}
        onPermissionModeChange={handlePermissionModeChange}
        onModelChange={v => handleModelOrEffortChange('model', v)}
        onEffortChange={v => handleModelOrEffortChange('effort', v)}
        onInterrupt={handleInterrupt}
        settingsLoading={settingsLoading.loading()}
        agentSessionInfo={agentSessionStore.getInfo(agentId())}
        agentWorking={agentStore.state.agents.find(a => a.id === agentId())?.status === AgentStatus.ACTIVE && isAgentWorking(chatStore.getMessages(agentId()))}
        containerHeight={props.containerHeight}
      />
    )
  }

  // Render a complete tile (tab bar + content) for the TilingLayout
  const renderTile = (tileId: string) => (
    <Tile
      tileId={tileId}
      isFocused={layoutStore.focusedTileId() === tileId}
      canClose={hasMultipleTiles()}
      canSplit={layoutStore.canSplitTile(tileId)}
      tabBar={createTabBarForTile(tileId)}
      onFocus={() => {
        layoutStore.setFocusedTile(tileId)
        // Sync global active tab to this tile's active tab
        const tab = getActiveTabForTile(tileId)
        if (tab) {
          tabStore.setActiveTab(tab.type, tab.id)
          if (tab.type === TabType.AGENT) {
            agentStore.setActiveAgent(tab.id)
          }
          else {
            terminalStore.setActiveTerminal(tab.id)
          }
        }
      }}
      onSplitHorizontal={() => {
        layoutStore.splitTileHorizontal(tileId)
        persistLayout()
      }}
      onSplitVertical={() => {
        layoutStore.splitTileVertical(tileId)
        persistLayout()
      }}
      onClose={() => {
        layoutStore.closeTile(tileId)
        persistLayout()
      }}
    >
      {renderTileContent(tileId)}
    </Tile>
  )

  const leftSidebarElement = (opts?: {
    isCollapsed: () => boolean
    onExpand: () => void
    onCollapse: () => void
    saveSidebarState?: () => void
    initialOpenSections?: Record<string, boolean>
    initialSectionSizes?: Record<string, number>
    onLeftStateChange?: (open: Record<string, boolean>, sizes: Record<string, number>) => void
  }) => (
    <LeftSidebar
      workspaces={workspaceStore.state.workspaces}
      activeWorkspaceId={workspace.activeWorkspaceId()}
      onSelectWorkspace={handleSelectWorkspace}
      onNewWorkspace={(sectionId) => {
        setNewWorkspaceTargetSectionId(sectionId)
        setShowNewWorkspace(true)
      }}
      onRefreshWorkspaces={() => loadWorkspaces()}
      onDeleteWorkspace={handleDeleteWorkspace}
      isCollapsed={opts?.isCollapsed() ?? false}
      onExpand={opts?.onExpand ?? (() => {})}
      onCollapse={opts?.onCollapse}
      initialOpenSections={opts?.initialOpenSections}
      initialSectionSizes={opts?.initialSectionSizes}
      onSectionStateChange={opts?.onLeftStateChange}
    />
  )

  const rightSidebarElement = (opts?: {
    isCollapsed: () => boolean
    onExpand: () => void
    onCollapse: () => void
    saveSidebarState?: () => void
    initialOpenSections?: Record<string, boolean>
    initialSectionSizes?: Record<string, number>
    onRightStateChange?: (open: Record<string, boolean>, sizes: Record<string, number>) => void
  }) => (
    <RightSidebar
      workspaceId={workspace.activeWorkspaceId() ?? ''}
      workerId={getCurrentTabContext().workerId}
      workingDir={getCurrentTabContext().workingDir}
      showTodos={showTodos()}
      activeTodos={activeTodos()}
      fileTreePath={fileTreePath()}
      onFileSelect={setFileTreePath}
      isCollapsed={opts?.isCollapsed() ?? false}
      onExpand={opts?.onExpand ?? (() => {})}
      onCollapse={opts?.onCollapse}
      initialOpenSections={opts?.initialOpenSections}
      initialSectionSizes={opts?.initialSectionSizes}
      onSectionStateChange={opts?.onRightStateChange}
    />
  )

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
              /* ---- Mobile layout ---- */
              <div class={styles.mobileShell}>
                {/* Overlay backdrop */}
                <Show when={leftSidebarOpen() || rightSidebarOpen()}>
                  <div class={styles.mobileOverlay} onClick={closeAllSidebars} />
                </Show>

                {/* Left sidebar overlay */}
                <div
                  class={styles.mobileSidebar}
                  classList={{ [styles.mobileSidebarOpen]: leftSidebarOpen() }}
                >
                  {leftSidebarElement()}
                </div>

                {/* Right sidebar overlay */}
                <div
                  class={`${styles.mobileSidebar} ${styles.mobileSidebarRight}`}
                  classList={{ [styles.mobileSidebarOpen]: rightSidebarOpen() }}
                >
                  {rightSidebarElement()}
                </div>

                {/* Center content - single tile on mobile */}
                <div class={styles.mobileCenter}>
                  {tabBarElement()}
                  {renderTileContent(layoutStore.focusedTileId())}
                  <Show when={focusedAgentId()}>
                    <FocusedAgentEditorPanel containerHeight={0} />
                  </Show>
                </div>
              </div>
            )}
          >
            {/* ---- Desktop layout ---- */}
            {(() => {
              // Read saved sidebar state before Resizable mounts (initialSize is read-once)
              const wsId = workspace.activeWorkspaceId()
              interface SidebarState {
                leftSize?: number
                rightSize?: number
                leftCollapsed?: boolean
                rightCollapsed?: boolean
                leftOpenSections?: Record<string, boolean>
                leftSectionSizes?: Record<string, number>
                rightOpenSections?: Record<string, boolean>
                rightSectionSizes?: Record<string, number>
              }
              const savedSidebar: SidebarState | null = (() => {
                if (!wsId)
                  return null
                try {
                  return JSON.parse(sessionStorage.getItem(`leapmux:sidebar:${wsId}`) ?? '')
                }
                catch { return null }
              })()
              const initLeft = savedSidebar?.leftSize ?? 0.18
              const initRight = savedSidebar?.rightSize ?? 0.20
              const initCenter = 1 - initLeft - initRight

              // Track inner section state for persistence
              let leftOpenSections: Record<string, boolean> = savedSidebar?.leftOpenSections ?? {}
              let leftSectionSizes: Record<string, number> = savedSidebar?.leftSectionSizes ?? {}
              let rightOpenSections: Record<string, boolean> = savedSidebar?.rightOpenSections ?? {}
              let rightSectionSizes: Record<string, number> = savedSidebar?.rightSectionSizes ?? {}

              // Ref to forward saveSidebarState into Resizable's onSizesChange
              let saveSidebarRef: (() => void) | undefined

              return (
                <Resizable orientation="horizontal" class={styles.shell} onSizesChange={() => saveSidebarRef?.()}>
                  {() => {
                    const ctx = Resizable.useContext()
                    const [leftCollapsed, setLeftCollapsed] = createSignal(false)
                    const [rightCollapsed, setRightCollapsed] = createSignal(false)
                    let leftSizeBeforeCollapse = initLeft
                    let rightSizeBeforeCollapse = initRight

                    // Sidebar state persistence (immediate save + debounced variant for resize)
                    const doSaveSidebarState = () => {
                      const id = workspace.activeWorkspaceId()
                      if (!id)
                        return
                      const sizes = ctx.sizes()
                      const state: SidebarState = {
                        leftSize: leftCollapsed() ? leftSizeBeforeCollapse : sizes[0],
                        rightSize: rightCollapsed() ? rightSizeBeforeCollapse : sizes[2],
                        leftCollapsed: leftCollapsed(),
                        rightCollapsed: rightCollapsed(),
                        leftOpenSections,
                        leftSectionSizes,
                        rightOpenSections,
                        rightSectionSizes,
                      }
                      sessionStorage.setItem(`leapmux:sidebar:${id}`, JSON.stringify(state))
                    }
                    let sidebarSaveTimer: ReturnType<typeof setTimeout> | null = null
                    const saveSidebarState = () => {
                      if (sidebarSaveTimer)
                        clearTimeout(sidebarSaveTimer)
                      sidebarSaveTimer = setTimeout(doSaveSidebarState, 300)
                    }
                    saveSidebarRef = saveSidebarState

                    const collapseLeft = () => {
                      leftSizeBeforeCollapse = ctx.sizes()[0] ?? initLeft
                      ctx.collapse(0)
                    }
                    const expandLeft = () => {
                      ctx.expand(0)
                      ctx.resize(0, leftSizeBeforeCollapse)
                    }
                    const collapseRight = () => {
                      rightSizeBeforeCollapse = ctx.sizes()[2] ?? initRight
                      ctx.collapse(2)
                    }
                    const expandRight = () => {
                      ctx.expand(2)
                      ctx.resize(2, rightSizeBeforeCollapse)
                    }

                    // Restore collapsed state after initial render
                    if (savedSidebar?.leftCollapsed)
                      queueMicrotask(() => collapseLeft())
                    if (savedSidebar?.rightCollapsed)
                      queueMicrotask(() => collapseRight())

                    return (
                      <>
                        {/* Left sidebar - Workspaces */}
                        <Resizable.Panel
                          initialSize={initLeft}
                          minSize={0.10}
                          collapsible
                          collapsedSize="45px"
                          collapseThreshold={0.05}
                          class={styles.sidebar}
                          onCollapse={() => {
                            setLeftCollapsed(true)
                            saveSidebarState()
                          }}
                          onExpand={() => {
                            setLeftCollapsed(false)
                            saveSidebarState()
                          }}
                        >
                          {leftSidebarElement({
                            isCollapsed: leftCollapsed,
                            onExpand: expandLeft,
                            onCollapse: collapseLeft,
                            saveSidebarState,
                            initialOpenSections: savedSidebar?.leftOpenSections,
                            initialSectionSizes: savedSidebar?.leftSectionSizes,
                            onLeftStateChange: (open, sizes) => {
                              leftOpenSections = open
                              leftSectionSizes = sizes
                              doSaveSidebarState()
                            },
                          })}
                        </Resizable.Panel>

                        <Resizable.Handle class={styles.resizeHandle} data-testid="resize-handle" />

                        {/* Center area */}
                        <Resizable.Panel
                          initialSize={initCenter}
                          class={styles.center}
                          ref={(el: HTMLElement) => {
                            const observer = new ResizeObserver((entries) => {
                              for (const entry of entries)
                                setCenterPanelHeight(entry.contentRect.height)
                            })
                            observer.observe(el)
                          }}
                        >
                          <Show
                            when={activeWorkspace() && !workspaceLoading()}
                            fallback={(
                              <Show when={!activeWorkspace() && !workspace.activeWorkspaceId()}>
                                <div class={styles.placeholder}>
                                  No workspaces yet. Click + to create one.
                                </div>
                              </Show>
                            )}
                          >
                            <CrossTileDragProvider
                              onIntraTileReorder={handleIntraTileReorder}
                              onCrossTileMove={handleCrossTileMove}
                              lookupTileIdForTab={lookupTileIdForTab}
                              renderDragOverlay={renderDragOverlay}
                            >
                              <TilingLayout
                                root={layoutStore.state.root}
                                renderTile={renderTile}
                                onRatioChange={(splitId, ratios) => {
                                  layoutStore.updateRatios(splitId, ratios)
                                  persistLayout()
                                }}
                              />
                            </CrossTileDragProvider>
                            <Show when={focusedAgentId()}>
                              <FocusedAgentEditorPanel containerHeight={centerPanelHeight()} />
                            </Show>
                          </Show>
                        </Resizable.Panel>

                        {/* Right sidebar - Files + To-dos */}
                        <Resizable.Handle class={styles.resizeHandle} data-testid="resize-handle" />
                        <Resizable.Panel
                          initialSize={initRight}
                          minSize={0.10}
                          collapsible
                          collapsedSize="45px"
                          collapseThreshold={0.05}
                          class={styles.rightPanel}
                          onCollapse={() => {
                            setRightCollapsed(true)
                            saveSidebarState()
                          }}
                          onExpand={() => {
                            setRightCollapsed(false)
                            saveSidebarState()
                          }}
                        >
                          {rightSidebarElement({
                            isCollapsed: rightCollapsed,
                            onExpand: expandRight,
                            onCollapse: collapseRight,
                            saveSidebarState,
                            initialOpenSections: savedSidebar?.rightOpenSections,
                            initialSectionSizes: savedSidebar?.rightSectionSizes,
                            onRightStateChange: (open, sizes) => {
                              rightOpenSections = open
                              rightSectionSizes = sizes
                              doSaveSidebarState()
                            },
                          })}
                        </Resizable.Panel>
                      </>
                    )
                  }}
                </Resizable>
              )
            })()}
          </Show>
        </div>
      </Show>

      <Show when={showResumeDialog()}>
        <ResumeSessionDialog
          defaultWorkerId={getCurrentTabContext().workerId}
          onResume={handleResumeAgent}
          onClose={() => setShowResumeDialog(false)}
        />
      </Show>

      <Show when={showNewAgentDialog()}>
        <NewAgentDialog
          workspaceId={activeWorkspace()?.id ?? ''}
          defaultWorkerId={getCurrentTabContext().workerId}
          defaultWorkingDir={getCurrentTabContext().workingDir}
          defaultTitle={`Agent ${nextTabNumber(tabStore.state.tabs, TabType.AGENT, 'Agent')}`}
          onCreated={(agent) => {
            setShowNewAgentDialog(false)
            const tileId = layoutStore.focusedTileId()
            agentStore.addAgent(agent)
            tabStore.addTab({
              type: TabType.AGENT,
              id: agent.id,
              title: agent.title || undefined,
              tileId,
              workerId: agent.workerId,
              workingDir: agent.workingDir,
            })
            tabStore.setActiveTabForTile(tileId, TabType.AGENT, agent.id)
            persistLayout()
            requestAnimationFrame(() => focusEditor?.())
          }}
          onClose={() => setShowNewAgentDialog(false)}
        />
      </Show>

      <Show when={showNewTerminalDialog()}>
        <NewTerminalDialog
          workspaceId={activeWorkspace()?.id ?? ''}
          defaultWorkerId={getCurrentTabContext().workerId}
          defaultWorkingDir={getCurrentTabContext().workingDir}
          onCreated={(terminalId, workerId, workingDir) => {
            setShowNewTerminalDialog(false)
            const ws = activeWorkspace()
            if (!ws)
              return
            const title = `Terminal ${nextTabNumber(tabStore.state.tabs, TabType.TERMINAL, 'Terminal')}`
            const tileId = layoutStore.focusedTileId()
            terminalStore.addTerminal({ id: terminalId, workspaceId: ws.id, workerId, workingDir })
            tabStore.addTab({ type: TabType.TERMINAL, id: terminalId, title, tileId, workerId, workingDir })
            tabStore.setActiveTabForTile(tileId, TabType.TERMINAL, terminalId)
            persistLayout()
          }}
          onClose={() => setShowNewTerminalDialog(false)}
        />
      </Show>

      <Show when={showNewWorkspace()}>
        <NewWorkspaceDialog
          preselectedWorkerId={preselectedWorkerId()}
          onCreated={(ws) => {
            workspaceStore.addWorkspace(ws)
            setShowNewWorkspace(false)
            setPreselectedWorkerId(undefined)
            const targetSectionId = newWorkspaceTargetSectionId()
            if (targetSectionId) {
              sectionClient.moveWorkspace({
                workspaceId: ws.id,
                sectionId: targetSectionId,
                position: 'N',
              }).catch(() => {}).finally(() => {
                setNewWorkspaceTargetSectionId(null)
                loadWorkspaces()
              })
            }
            else {
              loadWorkspaces()
            }
            navigate(`/o/${params.orgSlug}/workspace/${ws.id}`)
          }}
          onClose={() => {
            setShowNewWorkspace(false)
            setPreselectedWorkerId(undefined)
            setNewWorkspaceTargetSectionId(null)
          }}
        />
      </Show>

      <Show when={worktreeConfirm()}>
        {(confirm) => {
          let dlgRef!: HTMLDialogElement
          onMount(() => dlgRef.showModal())
          const handleCancel = () => {
            confirm().resolve('cancel')
            setWorktreeConfirm(null)
          }
          const handleKeep = () => {
            confirm().resolve('keep')
            setWorktreeConfirm(null)
          }
          const handleRemove = () => {
            confirm().resolve('remove')
            setWorktreeConfirm(null)
          }
          return (
            <dialog ref={dlgRef} class={dialogCompact} onClose={handleCancel}>
              <header><h2>Dirty Worktree</h2></header>
              <section>
                <p>The worktree has uncommitted changes or unpushed commits:</p>
                <p><code>{confirm().path}</code></p>
                <p>
                  Both the worktree and its branch
                  <code>{confirm().branchName}</code>
                  {' '}
                  will be deleted. Keep them on disk, or cancel?
                </p>
              </section>
              <footer>
                <button type="button" class="outline" onClick={handleCancel}>
                  Cancel
                </button>
                <button type="button" onClick={handleKeep}>
                  Keep
                </button>
                <ConfirmButton class="danger" onClick={handleRemove}>
                  Remove
                </ConfirmButton>
              </footer>
            </dialog>
          )
        }}
      </Show>
    </>
  )
}
