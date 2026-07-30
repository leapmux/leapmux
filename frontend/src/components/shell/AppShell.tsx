import type { Component } from 'solid-js'
import type { AppShellDialogStates, ChangeBranchState, DeleteBranchState, KeyPinConfirmState, NewWorkspacePayload, WorkspaceConfirmPayload } from './AppShellDialogs'
import type { SidebarElementsOpts } from './SidebarElements'
import type { TabContext } from './tabContext'
import type { CliPathStatus } from '~/api/platformBridge'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { SavedViewportScroll } from '~/stores/chatTypes'
import { useLocation, useSearchParams } from '@solidjs/router'
import { createEffect, createMemo, createSignal, on, onCleanup, Show } from 'solid-js'
import { isTauriApp, platformBridge } from '~/api/platformBridge'
import { apiLoadingTimeoutMs, workspaceBootstrapTimeoutMs } from '~/api/transport'
import { renameAgent, setExpectedUserId } from '~/api/workerRpc'
import { showWarnToast } from '~/components/common/Toast'
import { CliPathDialog } from '~/components/desktop/CliPathDialog'
import { isWorkspaceMutatable } from '~/components/shell/sectionUtils'
import { setTerminalScreenSink } from '~/components/terminal/TerminalView'
import { useAuth } from '~/context/AuthContext'
import { usePreferences } from '~/context/PreferencesContext'
import { TunnelProvider } from '~/context/TunnelContext'
import { useWorkspace } from '~/context/WorkspaceContext'
import { SectionType } from '~/generated/leapmux/v1/section_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createDialogState, createToggleDialog } from '~/hooks/createDialogState'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { useChatAutoFocus } from '~/hooks/useChatAutoFocus'
import { useIsMobileLayout } from '~/hooks/useIsMobileLayout'
import { useShortcuts } from '~/hooks/useShortcuts'
import { useVisualViewportInset } from '~/hooks/useVisualViewportInset'
import { useWorkspaceConnection } from '~/hooks/useWorkspaceConnection'
import { assertNever } from '~/lib/assertNever'
import { KEY_CLI_PATH_CHECKED, localStorageGet, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { hasWorkspaceDesktopChrome } from '~/lib/desktopChrome'
import { createImperativeRef } from '~/lib/imperativeRef'
import { createLogger } from '~/lib/logger'
import { setDashboardTitle, setWorkspaceTitle } from '~/lib/pageTitle'
import { parentDirectory } from '~/lib/paths'
import { createActiveClientStore } from '~/lib/presence/activeClient'
import { mountPresenceHeartbeat } from '~/lib/presence/heartbeat'
import { isMac } from '~/lib/shortcuts/platform'
import { printConsoleBanner } from '~/lib/systemInfo'
import { onlineWorkerIdSet, workerOnlineState } from '~/lib/workerLiveness'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import { createLayoutStore, useLayoutFocusSweep } from '~/stores/layout.store'
import { createSectionStore } from '~/stores/section.store'
import { agentTabToInfo, isTabReadyForGitStatus, tabKey } from '~/stores/tab.helpers'
import { createTabMetadataStore, useMetadataSweep } from '~/stores/tabMetadata.store'
import { createTabSelectionStore, useSelectionSweep } from '~/stores/tabSelection.store'
import { createTabView } from '~/stores/tabView'
import { workerInfoStore } from '~/stores/workerInfo.store'
import { createWorkspaceStore } from '~/stores/workspace.store'
import { AppShellDialogs } from './AppShellDialogs'
import { CustomTitlebar } from './CustomTitlebar'
import * as titlebarStyles from './CustomTitlebar.css'
import { DesktopLayout } from './DesktopLayout'
import { FloatingWindowLayer } from './FloatingWindowLayer'
import { GridPopoverHostProvider } from './GridPopoverHost'
import { handleBranchChanged } from './handleBranchChanged'
import { createMobileSidebarToggles, MobileLayout } from './MobileLayout'
import { mruAgentEditorDeps } from './mruAgentEditorDeps'
import { resolveActiveWorkspace } from './resolveActiveWorkspace'
import { createTabSelectionRestorer } from './restoreTabSelection'
import { createLeftSidebarElement, createRightSidebarElement } from './SidebarElements'
import { syncGitStatusToTabs, tabStampTarget } from './syncGitStatusToTabs'
import { activeWorkspaceKey } from './tabPersistenceKeys'
import { focusTile as focusTileShared } from './tileLifecycle'
import { createTileRenderer } from './TileRenderer'
import { useAgentOperations } from './useAgentOperations'
import { useCrdtRuntime } from './useCrdtRuntime'
import { useCrossWorkspaceMove } from './useCrossWorkspaceMove'
import { useFloatingWindowOps } from './useFloatingWindowOps'
import { useFocusInvariant } from './useFocusInvariant'
import { useTabHydrators } from './useTabHydrators'
import { useTabOperations } from './useTabOperations'
import { useTabPersistence } from './useTabPersistence'
import { useTerminalOperations } from './useTerminalOperations'
import { useTileDragDrop } from './useTileDragDrop'
import { useTurnEnd } from './useTurnEnd'
import { useWorkerPrivateStreams } from './useWorkerPrivateStreams'
import { useWorkerSection } from './useWorkerSection'
import { useWorkspaceLoader } from './useWorkspaceLoader'
import { createWorkspaceSwitcher } from './workspaceSwitcher'

const log = createLogger('shell')

export const AppShell: Component = () => {
  const auth = useAuth()
  const userId = () => auth.user()?.id ?? ''
  const workspace = useWorkspace()
  const preferences = usePreferences()
  const [searchParams, setSearchParams] = useSearchParams()
  const location = useLocation()

  printConsoleBanner()

  const workspaceStore = createWorkspaceStore()
  const sectionStore = createSectionStore()

  // Per-(workspace_id) active-client tracker fed by PresenceUpdate events off
  // the `/ws/userevents` WebSocket. Created here because the CRDT runtime feeds
  // it and `handleTurnEnd` reads it to gate the turn-end ding.
  const activeClient = createActiveClientStore()

  // Late-bound reload trigger for workspace-lifecycle events. Bound once
  // `useWorkspaceLoader` is instantiated further down; until then a lifecycle
  // event is a no-op, which is safe because the loader's own `onMount` seeds
  // the initial `listWorkspaces`.
  let reloadWorkspacesOnLifecycle: () => void = () => {}

  const {
    crdtState,
    projection,
    userEvents,
    effectiveClientId,
    ownClientId,
  } = useCrdtRuntime({
    userId: () => userId(),
    getWorkspaceId: () => workspace.activeWorkspaceId() ?? null,
    activeClient,
    onWorkspaceLifecycleChanged: () => reloadWorkspacesOnLifecycle(),
  })

  // Stores for what the CRDT does NOT carry. Tab metadata is keyed by tab id —
  // globally unique across the account — so one flat store holds every
  // workspace's worker-sourced fields with no active/inactive split.
  const tabMetadata = createTabMetadataStore()
  const tabView = createTabView({ projection, state: crdtState, metadata: tabMetadata })
  // Which tab is selected, per workspace and per tile. Client-local and
  // deliberately unsynced; survives a workspace switch in memory and a reload
  // via useTabPersistence.
  const selection = createTabSelectionStore(tabView, tabMetadata)
  const chatStore = createChatStore()
  const controlStore = createControlStore()
  const agentSessionStore = createAgentSessionStore()
  const layoutStore = createLayoutStore({
    getWorkspaceId: () => workspace.activeWorkspaceId(),
    projection,
  })
  const floatingWindowStore = createFloatingWindowStore({
    getWorkspaceId: () => workspace.activeWorkspaceId(),
    projection,
  })
  useFocusInvariant({ layoutStore, floatingWindowStore })
  const gitFileStatusStore = createGitFileStatusStore()
  const [fileTreePath, setFileTreePath] = createSignal('')
  // The watchdog gave up waiting for the bootstrap. Distinct from "not ready":
  // it only says the wait is over, never that the workspace arrived.
  const [bootstrapTimedOut, setBootstrapTimedOut] = createSignal(false)
  const [newAgentLoadingProvider, setNewAgentLoadingProvider] = createSignal<AgentProvider | null>(null)
  const [newTerminalLoading, setNewTerminalLoading] = createSignal(false)
  const [newShellLoading, setNewShellLoading] = createSignal(false)
  const settingsLoading = createLoadingSignal(apiLoadingTimeoutMs())
  // Dialog handles owned by AppShell. `lastTabConfirm` is owned by
  // useTabOperations (it drives the close flow) and joined into the
  // shared `dialogs` record below once tabOps exists.
  const newAgentDialog = createToggleDialog()
  const newTerminalDialog = createToggleDialog()
  const newWorkspaceDialog = createDialogState<NewWorkspacePayload>()
  const confirmDeleteWsDialog = createDialogState<WorkspaceConfirmPayload>()
  const confirmArchiveWsDialog = createDialogState<WorkspaceConfirmPayload>()
  const keyPinConfirmDialog = createDialogState<KeyPinConfirmState>()
  const changeBranchDialog = createDialogState<ChangeBranchState>()
  const deleteBranchDialog = createDialogState<DeleteBranchState>()
  // Set to a `missing` / `mismatch` status when the macOS PATH check should
  // show its dialog. `null` keeps the dialog unmounted (ok / unavailable /
  // not-yet-checked / non-macOS).
  const [cliPathInfo, setCliPathInfo] = createSignal<(CliPathStatus & { state: 'missing' | 'mismatch' }) | null>(null)

  const workerSection = useWorkerSection({
    getUserId: () => userId(),
    keyPinConfirmDialog,
  })

  // Tell the channel manager who this page thinks it is, so an open the Hub
  // authenticates as someone else fails instead of silently driving that user's
  // session with this page's UI. Read lazily: it must reflect the CURRENT user, not
  // whoever was signed in when the shell mounted — which is the whole divergence
  // this catches.
  setExpectedUserId(() => auth.user()?.id)

  // Publish `--vvh` (visible viewport height in px) for mobile layout.
  // No-op on desktop beyond a one-time write of window.innerHeight.
  useVisualViewportInset()

  // Mobile layout state
  const isMobileLayout = useIsMobileLayout()
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

  // Cache of file-tab paths fed by WatchWorkerPrivateEvents
  // bootstrap replay + GetFileTabPath fallback. Components consult
  // this for FILE-tab titles instead of asking the hub.

  // No reconciler. `tabView` joins the projection with `tabMetadata` on read, so
  // there is no second copy of tile_id / position / worker_id to drift, nothing
  // to drop or re-add on a remote change, and no active-workspace filter — every
  // workspace's tabs are derived from the same state at the same time.
  //
  // A tab whose tile_id names a node this client does not have yet is handled
  // inside `tabView`, by holding it at its last resolved placement. The hub
  // orders a split's EntityMaterialized frames before the batch that anchors a
  // tab to them, so the routine make-grid race is gone; see `tabView.ts` for
  // what genuinely remains.

  // Hydrate CRDT-projected tabs whose worker-side metadata (file path,
  // agent record, terminal title) hasn't arrived yet. The hook fires
  // off one best-effort fetch per pending tab; see useTabHydrators for
  // the per-type predicates.
  useTabHydrators({
    view: tabView,
    metadata: tabMetadata,
    // Worker liveness, so a worker coming back re-arms the tabs that gave up on
    // it. The sidebar already tracks this off `WORKERS_CHANGED`; hydration had
    // no other way to learn it, because a worker reconnecting changes nothing
    // about the candidate set.
    onlineWorkerIds: () => onlineWorkerIdSet(workerSection.workers()),
  })

  // Mount the input-driven heartbeat for the active workspace. The
  // returned `pingNow` is wired below to fire whenever the
  // `/ws/userevents` subscription completes its bootstrap — the hub's
  // PresenceUpdate broadcast only reaches subscribers, so a
  // heartbeat sent before the WS is connected never makes it back to
  // this client and the active-client gate stays empty.
  const heartbeat = mountPresenceHeartbeat({
    workspaceId: () => workspace.activeWorkspaceId() ?? '',
  })

  // Fire an immediate heartbeat on every stream (re)connect so the
  // hub's `received_at` is fresh against a subscription that can
  // actually receive the resulting broadcast. Tracks the
  // `bootstrapped` flip false → true.
  let lastBootstrapped = false
  createEffect(() => {
    const now = userEvents.bootstrapped()
    if (now && !lastBootstrapped)
      heartbeat.pingNow()
    lastBootstrapped = now
  })

  // One WatchWorkerPrivateEvents subscription per worker hosting a tab
  // anywhere in the account -- the Worker stores no workspace id, so there
  // is no narrower key. Drives the file-tab path cache and mirrors rename /
  // register / revoke events into the local stores.
  // See useWorkerPrivateStreams for the per-worker open/close logic.
  useWorkerPrivateStreams({
    view: tabView,
    metadata: tabMetadata,
  })

  // Late-bound ref: set once useTabOperations is initialized (after useWorkspaceConnection).
  let isAgentClosing: (agentId: string) => boolean = () => false
  const handleTurnEnd = useTurnEnd({
    preferences: {
      turnEndSound: () => preferences.turnEndSound(),
      turnEndSoundVolume: () => preferences.turnEndSoundVolume(),
    },
    activeClient,
    effectiveClientId,
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    ownClientId,
    setTurnEndTrigger,
    isAgentClosing: agentId => isAgentClosing(agentId),
  })

  // Streaming connection management
  useWorkspaceConnection({
    chatStore,
    view: tabView,
    metadata: tabMetadata,
    selection,
    controlStore,
    agentSessionStore,
    settingsLoading,
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    getWorkerId: () => {
      const tileId = layoutStore.focusedTileId()
      const tab = tileId ? selection.activeTabForTile(tileId) ?? null : null
      return tab?.workerId ?? ''
    },
    onTurnEnd: handleTurnEnd,
  })

  // Auto-open new workspace dialog from URL search params
  createEffect(() => {
    if (searchParams.newWorkspace === 'true') {
      newWorkspaceDialog.open({
        preselectedWorkerId: searchParams.workerId as string | undefined,
      })
      setSearchParams({ newWorkspace: undefined, workerId: undefined }, { replace: true })
    }
  })

  // Constant-true today: `(app).tsx` has exactly one leaf (`/`) and
  // hasWorkspaceDesktopChrome matches it, so every guard below always passes.
  // It is kept, not inlined away, because it is the predicate the documented
  // extension path in `(app).tsx` names: adding a non-workspace leaf under
  // `(app)/` is a type error at the layout (AppShell takes no children), but
  // nothing would stop these three EFFECTS from running on it -- and one of
  // them activates a workspace, mounting the whole per-workspace machinery
  // (CRDT bootstrap, tab fetch, presence heartbeat) on a route that wanted
  // none of it. Deleting them would make that extension silently hostile.
  const isWorkspaceRoute = createMemo(() => hasWorkspaceDesktopChrome(location.pathname))

  // macOS-only: when the user enters the workspace view in Solo mode, ask
  // the sidecar whether the bundled `leapmux` CLI is reachable on PATH.
  // Solo is the only mode where users invoke leapmux against the local hub
  // from a shell. The sessionStorage flag is set BEFORE the IPC so a slow
  // sidecar can't trigger duplicate prompts if the route flips during the
  // await — accepted trade-off: a transient sidecar failure suppresses the
  // prompt for the rest of the session.
  createEffect(() => {
    if (!isWorkspaceRoute())
      return
    if (!isTauriApp() || !isMac())
      return
    if (sessionStorageGet<boolean>(KEY_CLI_PATH_CHECKED))
      return
    void (async () => {
      const runtime = await platformBridge.getRuntimeState()
      if (runtime.shellMode !== 'solo')
        return
      sessionStorageSet(KEY_CLI_PATH_CHECKED, true)
      const status = await platformBridge.cliPathStatus()
      if (!status)
        return
      if (status.state === 'missing' || status.state === 'mismatch')
        setCliPathInfo(status)
    })()
  })

  // The only writer of WorkspaceContext's activeWorkspaceId. It flips the
  // signal and persists the new selection under this user's key — see
  // workspaceSwitcher for why the write has to happen there rather than in an
  // effect. Terminal scrollback is NOT captured here: a workspace switch is
  // only one of the ways a terminal's view goes away, so the capture lives in
  // `disposeTerminalInstance`, the teardown chokepoint every path reaches.
  const switchWorkspace = createWorkspaceSwitcher({
    getUserId: () => userId(),
    setActiveWorkspaceId: next => workspace.setActiveWorkspaceId(next),
  })

  // Where a disposing terminal's scrollback lands. Registered once, here,
  // because `TerminalView` owns the xterm instances but must not know about the
  // store graph — the same seam as `setCRDTBridge`. Every teardown path routes
  // through `disposeTerminalInstance`, so a workspace switch, a tab close, a
  // cross-workspace move and a floating-window close all preserve the buffer.
  setTerminalScreenSink((tabId, screen) => tabMetadata.patch(tabId, { screen }))
  onCleanup(() => setTerminalScreenSink(null))

  // Workspace & section loading
  const { loadWorkspaces, loadSections, handleMoveSection, handleMoveSectionServer } = useWorkspaceLoader({
    workspaceStore,
    sectionStore,
  })
  // Now that loadWorkspaces is in scope, wire it to the lifecycle
  // event callback above. Created/renamed/deleted events on the user
  // WS stream will refresh the sidebar without requiring a reconnect.
  reloadWorkspacesOnLifecycle = () => {
    void loadWorkspaces()
    void loadSections()
  }

  // Keep the active workspace pointed at one this user actually has. Runs on
  // load (nothing selected yet -> reopen where they left off) and whenever the
  // list changes underneath the selection (a delete from another device ->
  // fall back to a sibling, or to nothing when none is left). Since the URL no
  // longer carries a workspace id, this effect is the whole of that policy;
  // `resolveActiveWorkspace` holds the reasoning, including why an incomplete
  // or failed load must never move the user.
  //
  // This serializes a cold load behind `listWorkspaces`: nothing activates
  // until the list proves the saved id is still this user's. Adopting the saved
  // id optimistically would recover that wait and is deliberately not done — a
  // stale id would address a workspace the user no longer has, turning a silent
  // fallback into a permission-denied toast on every cold start.
  createEffect(() => {
    if (!isWorkspaceRoute())
      return
    // Skip while the NewWorkspaceDialog is open. The dialog races a
    // multi-step async flow (CreateWorkspace → openAgent → seedTab →
    // metadata seed) against the WorkspaceCreated event the hub
    // broadcasts inline during CreateWorkspace. Activating the new
    // workspace mid-flow would run the one-shot selection restore
    // against a workspace whose agent tab has not been placed yet,
    // burning the attempt and landing the user on the fallback tab.
    if (newWorkspaceDialog.value())
      return
    const uid = userId()
    const decision = resolveActiveWorkspace({
      activeWorkspaceId: workspace.activeWorkspaceId(),
      userId: uid,
      workspaceState: workspaceStore.state,
      savedWorkspaceId: uid ? localStorageGet<string>(activeWorkspaceKey(uid)) : undefined,
    })
    switch (decision.kind) {
      case 'keep':
        return
      case 'adopt':
        switchWorkspace(decision.workspaceId)
        return
      case 'clear':
        switchWorkspace(null)
        return
      default:
        assertNever(decision)
    }
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
    isWorkspaceMutatable(activeWorkspace() ?? undefined, isActiveWorkspaceArchived()),
  )

  // Active tab derived state
  const activeTab = createMemo(() => {
    const tileId = layoutStore.focusedTileId()
    return tileId ? selection.activeTabForTile(tileId) ?? null : null
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
    const tab = activeTab() ?? undefined
    return isTabReadyForGitStatus(tab, agentTabToInfo(tab))
  })

  // Get worker, working directory, and home directory from the currently active tab
  const getCurrentTabContext = (): TabContext => {
    const tab = activeTab()
    if (!tab)
      return { workerId: '', workingDir: '', homeDir: '', gitToplevel: '' }
    const workerId = tab.workerId ?? ''
    const homeDir = workerInfoStore.getHomeDir(workerId)
    const gitToplevel = tab.gitToplevel ?? ''
    if (tab.type === TabType.FILE) {
      const dir = tab.workingDir || (tab.filePath ? parentDirectory(tab.filePath) : '')
      return { workerId, workingDir: dir, homeDir, gitToplevel }
    }
    return { workerId, workingDir: tab.workingDir ?? '', homeDir, gitToplevel }
  }

  /**
   * Point the git-status singleton at the active tab's working tree.
   *
   * Two effects need exactly this — a turn ending, and the active tab's
   * context changing — and each used to hand-write the same three steps 460
   * lines apart. The gate is the subtle part: another agent's turn can fire
   * while the active tab is still inside its phase-0 window, so both must
   * defer until `activeTabReady` (see its definition for why).
   *
   * `clearWhenUnresolved` is the one genuine difference. A context change to a
   * tab with no working tree means there is nothing to show and the previous
   * repo's status must go; a turn ending in that state means nothing at all and
   * must leave the singleton alone.
   */
  const refreshGitStatusForActiveTab = (opts?: { clearWhenUnresolved?: boolean }) => {
    if (!activeTabReady())
      return
    const ctx = getCurrentTabContext()
    if (ctx.workerId && ctx.workingDir)
      gitFileStatusStore.refresh(ctx.workerId, ctx.workingDir)
    else if (opts?.clearWhenUnresolved)
      gitFileStatusStore.clear()
  }

  // Refresh git file status when a turn ends.
  createEffect(on(
    () => turnEndTrigger(),
    (_, prev) => {
      if (prev === undefined)
        return
      refreshGitStatusForActiveTab()
    },
  ))

  syncGitStatusToTabs({ gitFileStatusStore, view: tabView, metadata: tabMetadata })

  // Get working directory and home directory from the MRU agent tab
  const getMruAgentContext = (): Pick<TabContext, 'workingDir' | 'homeDir'> => {
    // Straight `.find` on the sorted tabs. Mapping every tab to a `${type}:${id}`
    // key first, matching that on a string prefix, then slicing the id back out
    // to re-look-up the tab allocated one string per tab and a second lookup for
    // the winner -- `mruOrder` already returns the assembled `Tab` objects.
    const agent = tabView.mruOrder(workspace.activeWorkspaceId() ?? '')
      .find(t => t.type === TabType.AGENT)
    if (!agent)
      return { workingDir: '', homeDir: '' }
    return {
      workingDir: agent.workingDir ?? '',
      homeDir: workerInfoStore.getHomeDir(agent.workerId ?? ''),
    }
  }

  const [leftSidebarVisible, setLeftSidebarVisible] = createSignal(true)
  const [rightSidebarVisible, setRightSidebarVisible] = createSignal(true)

  // Imperative refs for editor/scroll/sidebar callbacks. Each child
  // registers its impl via `.set(fn)`; readers invoke via `ref()?.()`.
  const toggleLeftSidebarRef = createImperativeRef<() => void>()
  const toggleRightSidebarRef = createImperativeRef<() => void>()
  const focusEditorRef = createImperativeRef<() => void>()
  const getScrollStateRef = createImperativeRef<() => SavedViewportScroll | undefined>()
  const forceScrollToBottomRef = createImperativeRef<() => void>()
  const [centerPanelHeight, setCenterPanelHeight] = createSignal(0)

  const agentOps = useAgentOperations({
    agentSessionStore,
    chatStore,
    controlStore,
    view: tabView,
    metadata: tabMetadata,
    selection,
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    layoutStore,
    settingsLoading,
    isActiveWorkspaceMutatable,
    activeWorkspace,
    getCurrentTabContext,
    newAgentDialog,
    setNewAgentLoadingProvider,
    focusEditor: () => focusEditorRef()?.(),
    forceScrollToBottom: () => forceScrollToBottomRef()?.(),
  })

  // Terminal operations hook
  const termOps = useTerminalOperations({
    view: tabView,
    metadata: tabMetadata,
    selection,
    layoutStore,
    activeWorkspace,
    isActiveWorkspaceMutatable,
    getCurrentTabContext,
    newTerminalDialog,
    setNewTerminalLoading,
    setNewShellLoading,
  })

  // Tab operations (select, close, file open, worktree confirm).
  // Owns the LastTabConfirmDialog because the close-flow drives it; hoist
  // the handle into the shared `dialogs` record so AppShellDialogs sees
  // one flat dialog map.
  const tabOps = useTabOperations({
    view: tabView,
    metadata: tabMetadata,
    selection,
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
    getActiveWorkspaceId: () => workspace.activeWorkspaceId() ?? undefined,
    workerOnlineState: workerId => workerOnlineState(workerSection.workers(), workerId),
  })
  // Build the final dialog map now that tabOps owns its handle.
  const dialogs: AppShellDialogStates = {
    newAgent: newAgentDialog,
    newTerminal: newTerminalDialog,
    newWorkspace: newWorkspaceDialog,
    confirmDeleteWs: confirmDeleteWsDialog,
    confirmArchiveWs: confirmArchiveWsDialog,
    lastTabConfirm: tabOps.lastTabConfirmDialog,
    keyPinConfirm: keyPinConfirmDialog,
    changeBranch: changeBranchDialog,
    deleteBranch: deleteBranchDialog,
  }
  // Bind the closing-agent check now that tabOps is available.
  isAgentClosing = (agentId: string) =>
    tabOps.closingTabKeys().has(tabKey({ type: TabType.AGENT, id: agentId }))

  // Fetch the worker-side half of a workspace's tabs: titles, agent status,
  // terminal screens, git fields. Structure needs no fetching — the projection
  // has every workspace's tabs already — so this is only ever about what the
  // CRDT does not carry.
  //
  // `useTabHydrators` above already covers this, for EVERY workspace: its
  // predicates run over `view.all()`, which is the whole projection now, and it
  // carries the inflight-dedupe and per-worker exponential backoff that a
  // fetch-on-activation effect does not. Adding a second, unguarded path here
  // meant a worker that was still handshaking got hammered on every reactive
  // tick until it closed the channel, which took the page down with it.
  //
  // So there is nothing left to trigger per workspace, and no `onExpandWorkspace`
  // fetch: expanding a sidebar row reveals tabs the hydrator is already working
  // on.

  // Has the CRDT bootstrap delivered this workspace? Shared by the restore and
  // the persistence writers so the two cannot disagree: whatever the reader
  // waits for is exactly what the writer waits for, and no third signal can let
  // one run without the other. See `useTabPersistence`'s module doc.
  const hasWorkspace = (wsId: string) => Boolean(projection()?.workspaces.has(wsId))

  // Read back the per-client pointers a reload wiped, so the user lands on the
  // tab they left rather than watching the MRU fallback pick one and then jump.
  const restoreTabSelection = createTabSelectionRestorer({
    selection,
    layoutStore,
    view: tabView,
    hasWorkspace,
  })

  createEffect(() => {
    const wsId = workspace.activeWorkspaceId()
    if (!wsId || !userId())
      return
    // Reads the projection, so this effect re-runs as the CRDT bootstrap lands
    // and the restore gets a real chance rather than one against an empty tree.
    restoreTabSelection(wsId)
    // Only AFTER the bootstrap has actually delivered this workspace.
    //
    // `activeWorkspaceId` comes from the `listWorkspaces` HTTP response, which
    // normally lands BEFORE the CRDT bootstrap arrives over the userevents
    // socket, so clearing the flag on that first pass would paint a placeholder
    // tree as a real one — and `DesktopLayout`'s action handlers would emit ops
    // naming a node the hub has never heard of.
    //
  })

  /**
   * Has the bootstrap delivered the workspace on screen? The ONE question the
   * tiling area paints on.
   *
   * Asked of the projection rather than tracked in a flag, so nothing can force
   * it true. That distinction is the whole point: `state.root` falls back to a
   * locally-minted placeholder leaf when the projection has no tree, and
   * painting that as real gives its action handlers a node the hub has never
   * heard of. The "open a new agent / terminal" affordances create the worker
   * resource BEFORE emitting the op, so the rejected batch leaves an orphaned
   * agent behind with no tab to reach it by.
   */
  const workspaceReady = createMemo(() => {
    const wsId = workspace.activeWorkspaceId()
    return !!wsId && hasWorkspace(wsId)
  })

  // Watchdog: never leave the shell silent forever on a bootstrap that never
  // arrives.
  //
  // Every step `workspaceReady` waits on can fail without raising anything.
  // `listWorkspaces` is plain HTTP and can succeed while the userevents socket
  // wedges or the hub restarts between the two calls; `useUserEvents` drops a
  // bootstrap whose user id does not match with nothing but a console warning; a
  // blocked network reconnects on a backoff forever. In every one of those
  // states `activeWorkspace()` is truthy while the projection stays empty, and
  // `DesktopLayout` renders neither the tiling area nor its no-workspace
  // fallback (that one needs no active workspace) — so the user got a blank
  // centre panel with no spinner, no error and no toast, reproducible across
  // reloads.
  //
  // This reports the FAILURE rather than forcing the gate. Clearing a shared
  // "loading" flag was the earlier shape, and it painted the placeholder tree as
  // real; the projection still fills in if it does eventually land, and
  // `workspaceReady` picks it up on its own.
  createEffect(() => {
    const wsId = workspace.activeWorkspaceId()
    if (!wsId || workspaceReady())
      return
    setBootstrapTimedOut(false)
    const timer = setTimeout(() => {
      log.warn('CRDT bootstrap did not deliver workspace', { workspaceId: wsId })
      setBootstrapTimedOut(true)
    }, workspaceBootstrapTimeoutMs())
    onCleanup(() => clearTimeout(timer))
  })

  useMetadataSweep(() => crdtState(), tabMetadata)
  // The tile owners, not a third walk of their trees: both stores memoize
  // "which tiles does this workspace have" per tick, and the sweep asks the
  // same question. Wrapped rather than passed unbound so the call site does not
  // depend on the stores being `this`-free.
  useSelectionSweep(() => projection(), selection, {
    tileOrderFor: wsId => layoutStore.tileOrderFor(wsId),
    getAllTileIdsFor: wsId => floatingWindowStore.getAllTileIdsFor(wsId),
  })
  useLayoutFocusSweep(() => projection(), layoutStore)

  // Mirror the selection / focus pointers to sessionStorage so the restore path
  // above can re-activate the user's last tab and tile after a refresh. Gated
  // on the same `hasWorkspace` the restore waits for — NOT on `workspaceLoading`,
  // which the watchdog clears on a timer: that would let a wedged bootstrap
  // authorise writes against empty state and delete the keys the restore has
  // not read yet.
  useTabPersistence({
    selection,
    layoutStore,
    floatingWindowStore,
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    hasWorkspace,
  })

  // Tile drag-and-drop
  const tileDrag = useTileDragDrop({ view: tabView, selection, layoutStore, floatingWindowStore })

  const focusTile = (tileId: string, workspaceId?: string) =>
    focusTileShared(layoutStore, floatingWindowStore, tileId, workspaceId)

  // --- Floating window tab movement operations ---
  const { handleDetachTab, handleAttachTab, handleToggleFloatingTab, handleActivateFloatingWindow }
    = useFloatingWindowOps({ layoutStore, floatingWindowStore, view: tabView, selection })

  const { move: handleCrossWorkspaceMove } = useCrossWorkspaceMove({
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    view: tabView,
    selection,
    layoutStore,
    floatingWindowStore,
    focusTile,
  })

  // Active agent todos (for right sidebar To-dos pane). "Active" is
  // derived from the active tab — if the user is looking at an AGENT
  // tab, that's the active agent.
  const activeTodos = createMemo(() => {
    const tab = activeTab()
    if (tab?.type !== TabType.AGENT)
      return []
    return chatStore.todos.get(tab.id)
  })

  const showTodos = createMemo(() => activeTabType() === TabType.AGENT && activeTodos().length > 0)

  // Workspace selection switches in place — there is no per-workspace URL.
  const handleSelectWorkspace = (id: string) => {
    closeAllSidebars()
    switchWorkspace(id)
  }

  // Handle workspace deletion
  const handleDeleteWorkspace = (deletedId: string, nextWorkspaceId: string | null) => {
    // No selection cleanup here. `useSelectionSweep` reclaims the deleted
    // workspace's pointers off the projection, which covers the delete this
    // client issued AND one issued on another device -- the latter arrives as a
    // lifecycle event with no local handler at all. Doing it here also never
    // worked: by this point the RPC and two list refreshes have been awaited,
    // so the hub's tombstone has usually already removed the workspace and the
    // tile-id lookup returned nothing.
    if (workspace.activeWorkspaceId() !== deletedId)
      return
    // No store to clear: the workspace's tabs leave the projection when the hub
    // tombstones them, and their metadata is swept by `retainOnly` on the next
    // tick. Only the selection pointer has to be dropped explicitly.
    // A null id clears the selection, which is what surfaces the
    // "Create a new workspace..." empty state once the last one is gone.
    switchWorkspace(nextWorkspaceId)
  }

  // Promise-based confirmation callbacks for workspace operations
  const handleConfirmDeleteWorkspace = (workspaceId: string): Promise<boolean> =>
    new Promise((resolve) => {
      confirmDeleteWsDialog.open({ workspaceId, resolve })
    })

  const handleConfirmArchiveWorkspace = (workspaceId: string): Promise<boolean> =>
    new Promise((resolve) => {
      confirmArchiveWsDialog.open({ workspaceId, resolve })
    })

  // Post-archive cleanup
  const handlePostArchiveWorkspace = (workspaceId: string) => {
    if (workspace.activeWorkspaceId() === workspaceId) {
      for (const tab of tabView.forWorkspace(workspaceId)) {
        if (tab.type === TabType.AGENT)
          controlStore.clearAgent(tab.id)
      }
    }
  }

  // Tile renderer (tab bars, tile content, editor panel)
  // One binding, shared by the tile renderer's quote/mention handlers and the
  // sidebar's file mention. All three used to hand-copy the same object literal.
  const mruEditorDeps = mruAgentEditorDeps({
    view: tabView,
    selection,
    layoutStore,
    floatingWindowStore,
    getWorkspaceId: () => workspace.activeWorkspaceId() ?? undefined,
  })

  const tileRenderer = createTileRenderer({
    mruEditorDeps,
    stores: {
      view: tabView,
      metadata: tabMetadata,
      selection,
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
      newAgentDialog,
      newTerminalDialog,
    },
    chrome: {
      isMobileLayout,
      toggleLeftSidebar,
      toggleRightSidebar,
    },
    refs: { focusEditorRef, getScrollStateRef, forceScrollToBottomRef },
    floatingWindow: {
      store: floatingWindowStore,
      onDetachTab: handleDetachTab,
      onAttachTab: handleAttachTab,
    },
    settingsLoading,
  })

  useChatAutoFocus(() => tileRenderer.focusedAgentId())

  useShortcuts({
    view: tabView,
    selection,
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    layoutStore,
    tabOps,
    agentOps,
    termOps,
    newAgentDialog,
    newTerminalDialog,
    newWorkspaceDialog,
    hasActiveWorkspace: () => activeWorkspace() !== null,
    toggleFloatingTab: handleToggleFloatingTab,
    toggleLeftSidebar: () => {
      if (isMobileLayout()) {
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
    mruEditorDeps,
    get workspaces() { return workspaceStore.state.workspaces },
    get activeWorkspaceId() { return workspace.activeWorkspaceId() },
    sectionStore,
    view: tabView,
    selection,
    loadSections,
    onSelectWorkspace: handleSelectWorkspace,
    onNewWorkspace: (sectionId: string | null) => {
      newWorkspaceDialog.open({ targetSectionId: sectionId })
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
      const active = selection.activeTabForWorkspace(workspace.activeWorkspaceId() ?? '')
      return active?.type === TabType.FILE ? active.filePath : undefined
    },
    get hasActiveFileTab() {
      const active = selection.activeTabForWorkspace(workspace.activeWorkspaceId() ?? '')
      return active?.type === TabType.FILE
    },
    get workers() { return workerSection.workers() },
    workerInfoFn: workerSection.workerInfoFn,
    channelStatusFn: workerSection.channelStatusFn,
    onAddTunnel: workerSection.openAddTunnel,
    onDeregisterWorker: workerSection.openWorkerSettings,
    onRegisterWorker: workerSection.openRegisterWorker,
    onTabClick: (type: number, id: string) => {
      // Route through the same handler as the TabBar so the sidebar inherits
      // its scroll-state save, batching, background-chat trim, and post-
      // switch focus side effects. Sidebar tabs come straight from the
      // store, so a missing lookup means the tab raced a close — bail.
      const tab = tabView.get(tabKey({ type: type as TabType, id }))
      if (tab)
        tabOps.handleTabSelect(tab)
    },
    tabItemOps: {
      onClose: tabOps.handleTabClose,
      onRename: (tab, title) => {
        tabMetadata.patch(tab.id, { title })
        if (tab.type === TabType.AGENT) {
          const workerId = tabView.getAgentTab(tab.id)?.workerId ?? ''
          renameAgent(workerId, { agentId: tab.id, title }).catch((err) => {
            showWarnToast('Failed to rename agent', err)
          })
        }
      },
      get closingKeys() { return tabOps.closingTabKeys() },
    },
    // Tile order for sidebar leaf ordering. No active/inactive fork: every
    // workspace's tree is in the projection. Returns `[]` when no layout is
    // projected yet (the CRDT bootstrap hasn't landed); the tree falls back to
    // a position-only sort.
    getTileOrderForWorkspace: (wsId: string) => layoutStore.tileOrderFor(wsId),
    onChangeBranch: ref => changeBranchDialog.open({
      workerId: ref.workerId,
      gitToplevel: ref.gitToplevel,
      workspaceId: ref.workspaceId,
      branchName: ref.branchName,
      isWorktree: ref.isWorktree,
    }),
    onDeleteBranch: ref => deleteBranchDialog.open({
      workerId: ref.workerId,
      gitToplevel: ref.gitToplevel,
      branchName: ref.branchName,
      tabs: ref.tabs,
    }),
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
    () => refreshGitStatusForActiveTab({ clearWhenUnresolved: true }),
  ))

  // The layer components close over the parent's scope so the outer JSX
  // stays flat — no nested `<Show>` ladders, no need to thread the dozens
  // of in-scope closures (handle*, tile/agent stores, layout store, etc.)
  // through props.
  //
  // The only decision left is desktop vs mobile. The "workspace not found"
  // arm went away with the per-workspace URL, and there is deliberately no arm
  // for a non-workspace route and no route outlet — see the note on the `(app)`
  // layout, which is where such a case would have to be reintroduced.

  const MobileShellLayer = () => (
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
  )

  const DesktopShellLayer = () => (
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
          workspaceReady={workspaceReady()}
          bootstrapTimedOut={bootstrapTimedOut()}
          getInProgressSectionId={() => sectionStore.getInProgressSection()?.id ?? null}
          onNewWorkspace={() => {
            newWorkspaceDialog.open({
              targetSectionId: sectionStore.getInProgressSection()?.id ?? null,
            })
          }}
          setCenterPanelHeight={setCenterPanelHeight}
          onIntraTileReorder={tileDrag.handleIntraTileReorder}
          onCrossTileMove={tileDrag.handleCrossTileMove}
          onCrossWorkspaceMove={handleCrossWorkspaceMove}
          lookupTileIdForTab={tileDrag.lookupTileIdForTab}
          renderDragOverlay={tileDrag.renderDragOverlay}
          renderTile={tileRenderer.renderTile}
          onRatioChange={(splitId, ratios) => layoutStore.updateRatios(splitId, ratios)}
          onGridRatiosChange={(gridId, axis, ratios) => layoutStore.updateGridRatios(gridId, axis, ratios)}
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
              view={tabView}
              selection={selection}
              renderTile={tileRenderer.renderTile}
              onRatioChange={(windowId, splitId, ratios) => floatingWindowStore.updateRatios(windowId, splitId, ratios)}
              onGridRatiosChange={(windowId, gridId, axis, ratios) => floatingWindowStore.updateGridRatios(windowId, gridId, axis, ratios)}
              onCloseWindow={tileRenderer.requestCloseFloatingWindow}
              onActivateWindow={handleActivateFloatingWindow}
              onFileDrop={tileRenderer.handleFileDrop}
              fileDropDisabled={tileRenderer.fileDropDisabled()}
            />
          )}
        />
      </div>
    </div>
  )

  const WorkspaceShellLayer = () => (
    <GridPopoverHostProvider>
      <div style={{ '--mono-font-family': preferences.monoFontFamily(), '--ui-font-family': preferences.uiFontFamily(), 'position': 'relative', 'height': '100%', 'width': '100%' }}>
        <Show when={!isMobileLayout()} fallback={<MobileShellLayer />}>
          <DesktopShellLayer />
        </Show>
      </div>
    </GridPopoverHostProvider>
  )

  return (
    <TunnelProvider store={workerSection.tunnelStore}>
      {/*
        Rendered unconditionally. There used to be a "workspace not found" arm
        here, for a URL naming a workspace the user does not own. No URL names
        one any more: the active id is either this user's own click on their own
        sidebar, or a saved id that `resolveActiveWorkspace` has already checked
        against the loaded list — so the dead-end 404 has no way to be reached,
        and the missing-workspace case became a fallback instead of a page.
      */}
      <WorkspaceShellLayer />

      <tileRenderer.CloseDialogs />

      <Show when={cliPathInfo()}>
        {info => (
          <CliPathDialog
            status={info()}
            onClose={() => setCliPathInfo(null)}
          />
        )}
      </Show>

      <AppShellDialogs
        dialogs={dialogs}
        onBranchChanged={(workerId, gitToplevel, newBranch) => handleBranchChanged(
          {
            target: tabStampTarget(tabView, tabMetadata),
            gitFileStatusStore,
            getCurrentTabContext,
          },
          workerId,
          gitToplevel,
          newBranch,
        )}
        activeWorkspace={activeWorkspace}
        getCurrentTabContext={getCurrentTabContext}
        agentOps={agentOps}
        termOps={termOps}
        tabOps={tabOps}
        view={tabView}
        metadata={tabMetadata}
        selection={selection}
        layoutStore={layoutStore}
        sectionStore={sectionStore}
        focusEditor={() => focusEditorRef()?.()}
        loadWorkspaces={loadWorkspaces}
        onSelectWorkspace={id => handleSelectWorkspace(id)}
        availableProviders={agentOps.availableProviders()}
        onRefreshProviders={agentOps.loadAvailableProviders}
      />

      <workerSection.Dialogs />

    </TunnelProvider>
  )
}
