import type { ParentComponent } from 'solid-js'
import type { KeyPinConfirmState } from './AppShellDialogs'
import type { SidebarElementsOpts } from './SidebarElements'
import type { TabContext } from './tabContext'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { Tab } from '~/stores/tab.store'
import { useLocation, useNavigate, useParams, useSearchParams } from '@solidjs/router'
import { createEffect, createMemo, createSignal, on, Show } from 'solid-js'
import { workerClient } from '~/api/clients'
import { listTabsForWorkspace } from '~/api/listTabsBatcher'
import { apiLoadingTimeoutMs } from '~/api/transport'
import { channelManager, moveTabWorkspace, renameAgent, setConfirmKeyPin, setGetUserId } from '~/api/workerRpc'
import { NotFoundPage } from '~/components/common/NotFoundPage'
import { showWarnToast } from '~/components/common/Toast'
import { isWorkspaceMutatable } from '~/components/shell/sectionUtils'
import { AddTunnelDialog } from '~/components/workers/AddTunnelDialog'
import { RegisterWorkerDialog } from '~/components/workers/RegisterWorkerDialog'
import { WorkerSettingsDialog } from '~/components/workers/WorkerSettingsDialog'
import { useAuth } from '~/context/AuthContext'
import { useOrg } from '~/context/OrgContext'
import { usePreferences } from '~/context/PreferencesContext'
import { TunnelProvider } from '~/context/TunnelContext'
import { useWorkspace } from '~/context/WorkspaceContext'
import { HubControlEvent } from '~/generated/leapmux/v1/channel_pb'
import { SectionType } from '~/generated/leapmux/v1/section_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { useChatAutoFocus } from '~/hooks/useChatAutoFocus'
import { useIsMobile } from '~/hooks/useIsMobile'
import { useShortcuts } from '~/hooks/useShortcuts'
import { useWorkspaceConnection } from '~/hooks/useWorkspaceConnection'
import { hasWorkspaceDesktopChrome } from '~/lib/desktopChrome'
import { createIdentityCache } from '~/lib/identityCache'
import { createImperativeRef } from '~/lib/imperativeRef'
import { createInflightCache } from '~/lib/inflightCache'
import { createLogger } from '~/lib/logger'
import { setDashboardTitle, setWorkspaceTitle } from '~/lib/pageTitle'
import { parentDirectory } from '~/lib/paths'
import { printConsoleBanner } from '~/lib/systemInfo'
import { createAgentStore } from '~/stores/agent.store'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import { createLayoutStore, firstLeafId } from '~/stores/layout.store'
import { createSectionStore } from '~/stores/section.store'
import { createTabStore, isTabReadyForGitStatus, preserveNonEmptyGitFields, protoToTerminalTab, tabKey } from '~/stores/tab.store'
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
import { GridPopoverHostProvider } from './GridPopoverHost'
import { createMobileSidebarToggles, MobileLayout } from './MobileLayout'
import { createLeftSidebarElement, createRightSidebarElement } from './SidebarElements'
import { syncGitStatusToTabs } from './syncGitStatusToTabs'
import { focusTile as focusTileShared, removeEmptyFloatingWindow } from './tileLifecycle'
import { createTileRenderer } from './TileRenderer'
import { useAgentOperations } from './useAgentOperations'
import { useTabOperations } from './useTabOperations'
import { useTabPersistence } from './useTabPersistence'
import { useTerminalOperations } from './useTerminalOperations'
import { useTileDragDrop } from './useTileDragDrop'
import { useWorkspaceLoader } from './useWorkspaceLoader'
import { useWorkspaceRestore } from './useWorkspaceRestore'
import { fanOutTabsToWorkers } from './workspaceTabHydration'

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
  const settingsLoading = createLoadingSignal(apiLoadingTimeoutMs())
  const [confirmDeleteWs, setConfirmDeleteWs] = createSignal<{ workspaceId: string, resolve: (confirmed: boolean) => void } | null>(null)
  const [confirmArchiveWs, setConfirmArchiveWs] = createSignal<{ workspaceId: string, resolve: (confirmed: boolean) => void } | null>(null)
  const [keyPinConfirm, setKeyPinConfirm] = createSignal<KeyPinConfirmState | null>(null)

  // Worker section state
  const workerInfoStore = createWorkerInfoStore()
  const workerChannelStatusStore = createWorkerChannelStatusStore(channelManager)
  const [workers, setWorkers] = createSignal<Worker[]>([])
  const [deregisterTarget, setDeregisterTarget] = createSignal<Worker | null>(null)
  const [addTunnelTarget, setAddTunnelTarget] = createSignal<Worker | null>(null)
  const [showRegisterWorker, setShowRegisterWorker] = createSignal(false)
  const tunnelStore = createTunnelStore()
  // listWorkers() returns freshly-deserialized objects on every call.
  // Stabilize identity by id so the sidebar's <For> doesn't unmount and
  // remount every worker row on each refresh / WORKERS_CHANGED push.
  const workerIdentity = createIdentityCache<Worker>({
    keyOf: w => w.id,
  })

  // Fetch workers list.
  async function fetchWorkers() {
    if (!org.orgId())
      return
    try {
      const resp = await workerClient.listWorkers({})
      const stable = workerIdentity.stabilize(resp.workers)
      setWorkers(stable)
      for (const w of stable) {
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
  const {
    leftSidebarOpen,
    rightSidebarOpen,
    toggleLeftSidebar,
    toggleRightSidebar,
    closeAllSidebars,
  } = createMobileSidebarToggles()

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
        return agentStore.getById(tab.id)?.workerId ?? ''
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

  const isWorkspaceRoute = createMemo(() => hasWorkspaceDesktopChrome(location.pathname))

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
    if (ws)
      setWorkspaceTitle(ws.title)
    else
      setDashboardTitle()
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

  // Whether the active tab's working tree is settled enough to query git
  // status. Used to gate every gitFileStatusStore.refresh — both the
  // workspace-list tab fields (via the createEffect below that mirrors
  // store → tab) and the Files section's per-row badges read from the
  // same store, so a single gate protects both. See isTabReadyForGitStatus
  // for the rationale on why we defer during a STARTING tab's phase-0
  // window.
  const activeTabReady = createMemo(() => {
    const tab = activeTab()
    const agent = tab?.type === TabType.AGENT ? agentStore.getById(tab.id) : null
    return isTabReadyForGitStatus(tab, agent)
  })

  // Get worker, working directory, and home directory from the currently active tab
  const getCurrentTabContext = (): TabContext => {
    const tab = activeTab()
    if (!tab)
      return { workerId: '', workingDir: '', homeDir: '' }
    if (tab.type === TabType.AGENT) {
      const agent = agentStore.getById(tab.id)
      const workerId = agent?.workerId || ''
      return { workerId, workingDir: agent?.workingDir ?? '', homeDir: agent?.homeDir ?? '' }
    }
    else if (tab.type === TabType.FILE) {
      const dir = tab.workingDir || (tab.filePath ? parentDirectory(tab.filePath) : '')
      const homeDir = agentStore.state.agents.find(a => a.workerId === tab.workerId)?.homeDir ?? ''
      return { workerId: tab.workerId ?? '', workingDir: dir, homeDir }
    }
    else {
      const workerId = tab.workerId ?? ''
      const homeDir = agentStore.state.agents.find(a => a.workerId === workerId)?.homeDir ?? ''
      return { workerId, workingDir: tab.workingDir ?? '', homeDir }
    }
  }

  // Refresh git file status when a turn ends. Another agent's turn can
  // fire while the active tab is still in its phase-0 window, so gate on
  // activeTabReady — see its definition for the rationale.
  createEffect(on(
    () => turnEndTrigger(),
    (_, prev) => {
      if (prev === undefined)
        return
      if (!activeTabReady())
        return
      const ctx = getCurrentTabContext()
      if (ctx.workerId && ctx.workingDir) {
        gitFileStatusStore.refresh(ctx.workerId, ctx.workingDir)
      }
    },
  ))

  syncGitStatusToTabs({ gitFileStatusStore, tabStore, agentStore })

  // Get working directory and home directory from the MRU agent tab
  const getMruAgentContext = (): Pick<TabContext, 'workingDir' | 'homeDir'> => {
    const agentPrefix = `${TabType.AGENT}:`
    const mruKey = tabStore.state.mruOrder.find(k => k.startsWith(agentPrefix))
    if (!mruKey)
      return { workingDir: '', homeDir: '' }
    const agentId = mruKey.slice(agentPrefix.length)
    const agent = agentStore.getById(agentId)
    return { workingDir: agent?.workingDir ?? '', homeDir: agent?.homeDir ?? '' }
  }

  const [leftSidebarVisible, setLeftSidebarVisible] = createSignal(true)
  const [rightSidebarVisible, setRightSidebarVisible] = createSignal(true)

  // Imperative refs for editor/scroll/sidebar callbacks. Each child
  // registers its impl via `.set(fn)`; readers invoke via `ref()?.()`.
  const toggleLeftSidebarRef = createImperativeRef<() => void>()
  const toggleRightSidebarRef = createImperativeRef<() => void>()
  const focusEditorRef = createImperativeRef<() => void>()
  const getScrollStateRef = createImperativeRef<() => { distFromBottom: number, atBottom: boolean } | undefined>()
  const forceScrollToBottomRef = createImperativeRef<() => void>()
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
    focusEditor: () => focusEditorRef()?.(),
    forceScrollToBottom: () => forceScrollToBottomRef()?.(),
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
    focusEditor: () => focusEditorRef()?.(),
    getScrollState: () => getScrollStateRef()?.(),
    setFileTreePath,
  })
  // Bind the closing-agent check now that tabOps is available.
  isAgentClosing = (agentId: string) =>
    tabOps.closingTabKeys().has(tabKey({ type: TabType.AGENT, id: agentId }))

  // Dedupes concurrent lazy-load requests per workspace so two sidebar
  // instances (left + right) firing the same expand effect don't issue
  // duplicate ListTabs + downstream listAgents/listTerminals fan-outs.
  //
  // Distinct from the registry snapshot's `tabsLoaded` flag: that flag flips
  // to true only after the fetch succeeds and the full snapshot (tabs,
  // agents, layout) is written, which is what downstream readers observe.
  // This cache bridges the gap between the RPC starting and that snapshot
  // landing — flipping `tabsLoaded` early would lie to consumers about
  // `tabs: []`. Once the snapshot is written, `tabsLoaded` takes over and
  // the inflight entry is cleared automatically.
  const tabsLoadInflight = createInflightCache<string, void>()

  // Lazy-load tabs for a non-active workspace when its tree is expanded.
  // Declared before useWorkspaceRestore so the restore path can fire it for
  // sibling workspaces in the same microtask as the active workspace's
  // ListTabs — the batcher then coalesces them into one RPC.
  const handleExpandWorkspace = (workspaceId: string) => {
    const snap = registry.get(workspaceId)
    if (snap?.tabsLoaded)
      return
    if (tabsLoadInflight.has(workspaceId))
      return
    const currentOrgId = org.orgId()
    if (!currentOrgId)
      return

    void tabsLoadInflight.run(workspaceId, async () => {
      try {
        const tabsResp = await listTabsForWorkspace(currentOrgId, workspaceId)
        const { agents, terminalsByWorker } = await fanOutTabsToWorkers(tabsResp.tabs)
        const anyTerminalFetchFailed = terminalsByWorker.some(r => r.terminals === null)

        // Index the previous snapshot's tabs so we can preserve
        // gitBranch/gitOriginUrl across a transient BatchGetGitStatus
        // miss. Matches the hydration behaviour in useWorkspaceRestore.
        const existing = registry.get(workspaceId)
        const previousTabsByKey = new Map<string, Tab>()
        if (existing) {
          for (const t of existing.tabs) {
            previousTabsByKey.set(tabKey(t), t)
          }
        }

        const tabs: Tab[] = []
        for (const a of agents) {
          const fresh: Tab = {
            type: TabType.AGENT,
            id: a.id,
            title: a.title || undefined,
            workerId: a.workerId,
            workingDir: a.workingDir,
            agentProvider: a.agentProvider,
            gitBranch: a.gitStatus?.branch || undefined,
            gitOriginUrl: a.gitStatus?.originUrl || undefined,
            gitToplevel: a.gitStatus?.toplevel || undefined,
          }
          const previous = previousTabsByKey.get(tabKey(fresh))
          tabs.push({ ...fresh, ...preserveNonEmptyGitFields(fresh, previous) })
        }
        for (const { workerId, terminals } of terminalsByWorker) {
          if (terminals === null)
            continue
          for (const t of terminals) {
            const fresh = protoToTerminalTab(workerId, t)
            const previous = previousTabsByKey.get(tabKey(fresh))
            tabs.push({ ...fresh, ...preserveNonEmptyGitFields(fresh, previous) })
          }
        }

        // When a terminal fetch fails, preserve the previous terminal tabs (if any)
        // so they don't disappear from the sidebar on a transient error. An empty
        // successful result means the worker truly has no terminals.
        if (anyTerminalFetchFailed && existing) {
          const freshTerminalIds = new Set<string>()
          for (const t of tabs) {
            if (t.type === TabType.TERMINAL)
              freshTerminalIds.add(t.id)
          }
          for (const t of existing.tabs) {
            if (t.type === TabType.TERMINAL && !freshTerminalIds.has(t.id))
              tabs.push(t)
          }
        }
        registry.set(workspaceId, {
          workspaceId,
          tabs,
          activeTabKey: existing?.activeTabKey ?? null,
          layout: existing?.layout ?? { root: { type: 'leaf', id: 'default' }, focusedTileId: null },
          agents,
          restored: false,
          tabsLoaded: true,
        })
      }
      catch (err) {
        // Transient: the sidebar will re-expand and retry on the next user
        // interaction. Worth a warn so flaky hubs don't fail silently.
        log.warn('failed to lazy-load tabs for workspace', { workspaceId, err })
      }
    })
  }

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
    onExpandWorkspace: handleExpandWorkspace,
  })

  // Tile drag-and-drop
  const tileDrag = useTileDragDrop({ tabStore, layoutStore, floatingWindowStore, persistLayout })

  const focusTile = (tileId: string) => focusTileShared(layoutStore, floatingWindowStore, tileId)

  // --- Floating window tab movement operations ---
  const handleDetachTab = (tab: Tab) => {
    const sourceTileId = tab.tileId
    const { tileId } = floatingWindowStore.addWindow()
    tabStore.moveTabToTile(tabKey(tab), tileId)
    tabStore.setActiveTabForTile(tileId, tab.type, tab.id)
    // Close the source tile if it's now empty and the main layout has multiple tiles
    if (sourceTileId
      && tabStore.getTabsForTile(sourceTileId).length === 0
      && layoutStore.hasMultipleTiles()) {
      layoutStore.closeTile(sourceTileId)
      tabStore.cleanupTile(sourceTileId)
    }
    focusTile(tileId)
    persistLayout()
  }

  const handleAttachTab = (tab: Tab) => {
    const sourceTileId = tab.tileId
    if (!sourceTileId || !floatingWindowStore.getWindowForTile(sourceTileId))
      return

    const targetTileId = firstLeafId(layoutStore.state.root)
    if (!targetTileId)
      return

    tabStore.moveTabToTile(tabKey(tab), targetTileId)
    tabStore.setActiveTabForTile(targetTileId, tab.type, tab.id)
    layoutStore.setFocusedTile(targetTileId)
    removeEmptyFloatingWindow(layoutStore, floatingWindowStore, tabStore, sourceTileId)
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
      tab = tabStore.getTabByKey(draggedKey)
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
      workerId = agentStore.getById(tab.id)?.workerId ?? ''
    }

    // Remove the tab from the source (optimistic UI update).
    if (isSourceActive) {
      tabStore.removeTab(tab.type, tab.id)
    }
    else {
      registry.removeTab(resolvedSourceWsId, tab)
    }

    // If the source floating window is now empty, remove it.
    if (isSourceActive)
      removeEmptyFloatingWindow(layoutStore, floatingWindowStore, tabStore, tab.tileId)

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

      // Honor the caller-provided tile when given (sidebar drop on a specific
      // tile); otherwise fall back to the snapshot's focused tile / first leaf.
      const snapTileId = targetTileId
        ?? targetSnap.layout.focusedTileId
        ?? firstLeafId(targetSnap.layout.root)
        ?? ''
      const newTab = { ...tab, tileId: snapTileId }
      const key = tabKey(newTab)
      const movedAgent = tab.type === TabType.AGENT
        ? agentStore.getById(tab.id)
        : undefined
      const nextAgents = movedAgent && !targetSnap.agents.some(a => a.id === tab.id)
        ? [...targetSnap.agents, movedAgent]
        : targetSnap.agents
      registry.set(resolvedTargetWsId, {
        ...targetSnap,
        tabs: [...targetSnap.tabs, newTab],
        activeTabKey: key,
        mruOrder: [key, ...(targetSnap.mruOrder ?? [])],
        tileActiveTabKeys: snapTileId
          ? { ...(targetSnap.tileActiveTabKeys ?? {}), [snapTileId]: key }
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
          const resp = await listTabsForWorkspace(currentOrgId, resolvedTargetWsId)
          const existingKeys = new Set(targetSnap.tabs.map(t => tabKey(t)))
          const extraTabs: Tab[] = []
          for (const t of resp.tabs) {
            const tab: Tab = {
              type: t.tabType as TabType,
              id: t.tabId,
              position: t.position,
              tileId: t.tileId || targetSnap.layout.focusedTileId || '',
              workerId: t.workerId,
            }
            if (!existingKeys.has(tabKey(tab))) {
              extraTabs.push(tab)
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
            const agent = agentStore.getById(tab!.id)
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
    stores: {
      tabStore,
      agentStore,
      chatStore,
      controlStore,
      layoutStore,
      agentSessionStore,
      gitFileStatusStore,
    },
    ops: { agentOps, termOps },
    workspace: {
      isActiveWorkspaceMutatable,
      isActiveWorkspaceArchived,
      activeWorkspace,
      getCurrentTabContext,
      getMruAgentContext,
    },
    tab: {
      handleTabSelect: tabOps.handleTabSelect,
      handleTabClose: tabOps.handleTabClose,
      setIsTabEditing: tabOps.setIsTabEditing,
      closingTabKeys: tabOps.closingTabKeys,
    },
    newTab: {
      newAgentLoadingProvider,
      newTerminalLoading,
      newShellLoading,
      setShowNewAgentDialog,
      setShowNewTerminalDialog,
    },
    chrome: {
      isMobile,
      toggleLeftSidebar,
      toggleRightSidebar,
    },
    refs: { focusEditorRef, getScrollStateRef, forceScrollToBottomRef },
    floatingWindow: {
      store: floatingWindowStore,
      onDetachTab: handleDetachTab,
      onAttachTab: handleAttachTab,
    },
    persistLayout,
    settingsLoading,
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
        toggleLeftSidebarRef()?.()
      }
    },
    toggleRightSidebar: () => toggleRightSidebarRef()?.(),
    activeTabType,
    resolveFocusedTab: tileRenderer.resolveFocusedTab,
    splitFocusedTile: tileRenderer.splitFocusedTile,
    scrollFocusedTabPage: tileRenderer.scrollFocusedTabPage,
    writeToFocusedTerminal: tileRenderer.writeToFocusedTerminal,
    getCurrentTabContext,
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
    get activeTabReady() { return activeTabReady() },
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
    onRegisterWorker: () => setShowRegisterWorker(true),
    onTabClick: (type: number, id: string) => {
      // Route through the same handler as the TabBar so the sidebar inherits
      // its scroll-state save, batching, background-chat trim, and post-
      // switch focus side effects. Sidebar tabs come straight from the
      // store, so a missing lookup means the tab raced a close — bail.
      const tab = tabStore.getTabByKey(tabKey({ type: type as TabType, id }))
      if (tab)
        tabOps.handleTabSelect(tab)
    },
    tabItemOps: {
      onClose: tabOps.handleTabClose,
      onRename: (tab, title) => {
        tabStore.updateTabTitle(tab.type, tab.id, title)
        if (tab.type === TabType.AGENT) {
          const workerId = agentStore.getById(tab.id)?.workerId ?? ''
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
  // (not on every tab switch within the same worker context). Including
  // activeTabReady in the dep key lets the effect re-fire when a STARTING
  // tab finishes its phase-0 window — see activeTabReady for why we defer.
  createEffect(on(
    () => {
      const ctx = getCurrentTabContext()
      return `${ctx.workerId}\0${ctx.workingDir}\0${activeTabReady() ? '1' : '0'}`
    },
    () => {
      if (!activeTabReady())
        return
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
        <GridPopoverHostProvider>
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
                  onToggleLeftSidebar={() => toggleLeftSidebarRef()?.()}
                  onToggleRightSidebar={() => toggleRightSidebarRef()?.()}
                  leftSidebarVisible={leftSidebarVisible()}
                  rightSidebarVisible={rightSidebarVisible()}
                  activeWorkingDir={() => getCurrentTabContext().workingDir || undefined}
                />
                <div class={titlebarStyles.titlebarContent}>
                  <DesktopLayout
                    setToggleLeftSidebar={fn => toggleLeftSidebarRef.set(fn)}
                    setToggleRightSidebar={fn => toggleRightSidebarRef.set(fn)}
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
                      if (layoutStore.updateRatios(splitId, ratios))
                        persistLayout()
                    }}
                    onGridRatiosChange={(gridId, axis, ratios) => {
                      if (layoutStore.updateGridRatios(gridId, axis, ratios))
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
                          if (floatingWindowStore.updateRatios(windowId, splitId, ratios))
                            persistLayout()
                        }}
                        onGridRatiosChange={(windowId, gridId, axis, ratios) => {
                          if (floatingWindowStore.updateGridRatios(windowId, gridId, axis, ratios))
                            persistLayout()
                        }}
                        onCloseWindow={tileRenderer.requestCloseFloatingWindow}
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
        </GridPopoverHostProvider>
      </Show>

      <tileRenderer.CloseDialogs />

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
        focusEditor={() => focusEditorRef()?.()}
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

      <Show when={showRegisterWorker()}>
        <RegisterWorkerDialog onClose={() => setShowRegisterWorker(false)} />
      </Show>
    </TunnelProvider>
  )
}
