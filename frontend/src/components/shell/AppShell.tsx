import type { Component } from 'solid-js'
import type { AppShellDialogStates, ChangeBranchState, DeleteBranchState, EmptyArchiveConfirmPayload, KeyPinConfirmState, NewTabTarget, NewWorkspacePayload, SectionConfirmPayload, WorkspaceConfirmPayload } from './AppShellDialogs'
import type { SectionNamePayload } from './SectionNameDialog'
import type { SectionActions } from './sectionUtils'
import type { SidebarElementsOpts } from './SidebarElements'
import type { TabContext } from './tabContext'
import type { CliPathStatus } from '~/api/platformBridge'
import type { BranchRefActions } from '~/components/workspace/branchActions'
import type { WorkspaceStartActions, WorkspaceStartAt } from '~/components/workspace/workspaceStartActions'
import type { WorkspaceStartPoint } from '~/components/workspace/workspaceStartPoint'
import type { BranchRef } from '~/components/workspace/WorkspaceTabTree'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ChangeBranchMode } from '~/hooks/useGitModeState'
import type { SavedViewportScroll } from '~/stores/chatTypes'
import { useLocation, useSearchParams } from '@solidjs/router'
import { createEffect, createMemo, createResource, createSignal, on, onCleanup, onMount, Show } from 'solid-js'
import { getRuntimeState, isTauriApp, platformBridge } from '~/api/platformBridge'
import { apiLoadingTimeoutMs, workspaceBootstrapTimeoutMs } from '~/api/transport'
import { setExpectedUserId } from '~/api/workerRpc'
import { BootSplash } from '~/components/common/BootSplash'
import { CliPathDialog } from '~/components/desktop/CliPathDialog'
import { isWorkspaceMutatable } from '~/components/shell/sectionUtils'
import { setTerminalScreenSink } from '~/components/terminal/TerminalView'
import { useAuth } from '~/context/AuthContext'
import { usePreferences } from '~/context/PreferencesContext'
import { TunnelProvider } from '~/context/TunnelContext'
import { useWorkspace } from '~/context/WorkspaceContext'
import { SectionType } from '~/generated/proto/leapmux/v1/section_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createDialogState } from '~/hooks/createDialogState'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { useChatAutoFocus } from '~/hooks/useChatAutoFocus'
import { useIsMobileLayout } from '~/hooks/useIsMobileLayout'
import { useShortcuts } from '~/hooks/useShortcuts'
import { useVisualViewportInset } from '~/hooks/useVisualViewportInset'
import { useWorkspaceConnection } from '~/hooks/useWorkspaceConnection'
import { assertNever } from '~/lib/assertNever'
import { BOOT_PHASE_READY, BOOT_SPLASH_ENTER_MS, setBootPhase, setBootShell } from '~/lib/bootSplashTheme'
import { KEY_ACTIVE_WORKSPACE, KEY_CLI_PATH_CHECKED, localStorageGet, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { getCRDTBridge } from '~/lib/crdt'
import { hasWorkspaceDesktopChrome } from '~/lib/desktopChrome'
import { attachDismissSoftKeyboardOnTap } from '~/lib/dismissSoftKeyboardOnTap'
import { createImperativeRef } from '~/lib/imperativeRef'
import { createLogger } from '~/lib/logger'
import { setDashboardTitle, setWorkspaceTitle } from '~/lib/pageTitle'
import { parentDirectory } from '~/lib/paths'
import { prefersReducedMotion } from '~/lib/prefersReducedMotion'
import { createActiveClientStore } from '~/lib/presence/activeClient'
import { mountPresenceHeartbeat } from '~/lib/presence/heartbeat'
import { isMac } from '~/lib/shortcuts/platform'
import { printConsoleBanner } from '~/lib/systemInfo'
import { isWorkerKnownOnline, onlineWorkerIdSet, workerOnlineState } from '~/lib/workerLiveness'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { shouldShowBackgroundTasksSection } from '~/stores/chatBackgroundTasks'
import { createControlStore } from '~/stores/control.store'
import { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import { createLayoutStore, useLayoutFocusSweep } from '~/stores/layout.store'
import { focusedRepoKeyFromTab, gitStatusProbePath } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { createSectionStore } from '~/stores/section.store'
import { agentTabToInfo, isTabReadyForGitStatus, mruSteerableAgentTab, rootAgentIdFor, tabKey } from '~/stores/tab.helpers'
import { createTabMetadataStore, useMetadataSweep } from '~/stores/tabMetadata.store'
import { createTabSelectionStore, useSelectionSweep } from '~/stores/tabSelection.store'
import { createTabView } from '~/stores/tabView'
import { workerInfoStore } from '~/stores/workerInfo.store'
import { createWorkspaceStore } from '~/stores/workspace.store'
import * as styles from './AppShell.css'
import { AppShellDialogs } from './AppShellDialogs'
import { CustomTitlebar } from './CustomTitlebar'
import * as titlebarStyles from './CustomTitlebar.css'
import { DesktopLayout } from './DesktopLayout'
import { FloatingWindowLayer } from './FloatingWindowLayer'
import { GridPopoverHostProvider } from './GridPopoverHost'
import { handleBranchChanged } from './handleBranchChanged'
import { createMobileOverlayState, MobileLayout } from './MobileLayout'
import { mruAgentEditorDeps } from './mruAgentEditorDeps'
import { openSubagentTab } from './openSubagentTab'
import { renameTab } from './renameTab'
import { resolveActiveWorkspace } from './resolveActiveWorkspace'
import { createTabSelectionRestorer } from './restoreTabSelection'
import { SectionDragProvider } from './SectionDragContext'
import { isShellReady } from './shellReady'
import { createLeftSidebarElement, createRightSidebarElement } from './SidebarElements'
import { TabDragProvider } from './TabDragContext'
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
import { WorkspaceCenterFallback } from './WorkspaceCenterFallback'
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
  const repoGitStore = createRepoGitStore()
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
  const newAgentDialog = createDialogState<NewTabTarget>()
  const newTerminalDialog = createDialogState<NewTabTarget>()
  const newWorkspaceDialog = createDialogState<NewWorkspacePayload>()
  const confirmDeleteWsDialog = createDialogState<WorkspaceConfirmPayload>()
  const confirmArchiveWsDialog = createDialogState<WorkspaceConfirmPayload>()
  const confirmEmptyArchiveDialog = createDialogState<EmptyArchiveConfirmPayload>()
  const sectionNameDialog = createDialogState<SectionNamePayload>()
  const confirmDeleteSectionDialog = createDialogState<SectionConfirmPayload>()
  const keyPinConfirmDialog = createDialogState<KeyPinConfirmState>()
  const changeBranchDialog = createDialogState<ChangeBranchState>()
  const deleteBranchDialog = createDialogState<DeleteBranchState>()
  // Both branch surfaces -- the sidebar's branch row and the composer's branch
  // chip -- run the same actions from the same BranchRef, so the adapters are
  // defined once. A field added to either dialog state then reaches both.
  const openChangeBranchDialog = (ref: BranchRef, initialMode: ChangeBranchMode) => changeBranchDialog.open({
    workerId: ref.workerId,
    gitToplevel: ref.gitToplevel,
    workspaceId: ref.workspaceId,
    branchName: ref.branchName,
    isWorktree: ref.isWorktree,
    initialMode,
  })
  const openDeleteBranchDialog = (ref: BranchRef) => deleteBranchDialog.open({
    workerId: ref.workerId,
    gitToplevel: ref.gitToplevel,
    branchName: ref.branchName,
    isWorktree: ref.isWorktree,
    tabs: ref.tabs,
  })
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

  // Publish `--vvh` / `--vv-shift` (the visible region's size and place) for
  // the mobile layout, and report whether the soft keyboard is up. No-op on a
  // fine-pointer device, which has no keyboard to take screen space.
  const softKeyboardUp = useVisualViewportInset()
  // One recognizer for every surface: a short still tap outside an editor
  // puts the on-screen keyboard away. `dismissSoftKeyboard` is a no-op when
  // no keyboard is covering the screen.
  onCleanup(attachDismissSoftKeyboardOnTap())

  // Mobile layout state. The overlay state is the ONE owner of "which
  // overlay is up" (drawers, tab sheet); exclusion between them is structural
  // in createMobileOverlayState, not a close-the-other call at each toggle.
  const isMobileLayout = useIsMobileLayout()
  const mobileOverlayState = createMobileOverlayState()
  const {
    overlay: mobileOverlay,
    toggleDrawer: toggleMobileDrawer,
    closeSheet: closeMobileSheet,
    closeAll: closeMobileOverlay,
    applySwipe: applyMobileSwipe,
  } = mobileOverlayState

  // Shared turn-end signal: bumped when an agent turn ends.
  // Drives sound playback, git file status refresh, and directory tree refresh.
  const [turnEndTrigger, setTurnEndTrigger] = createSignal(0)

  // No reconciler. `tabView` joins the projection with `tabMetadata` on read, so
  // there is no second copy of tile_id / position / worker_id to drift, nothing
  // to drop or re-add on a remote change, and no active-workspace filter — every
  // workspace's tabs are derived from the same state at the same time.
  //
  // A tab whose tile_id identifies a node this client does not have yet is handled
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
    repoGitStore,
    // Worker liveness, so a worker coming back re-arms the tabs that gave up on
    // it. The sidebar already tracks this off `WORKERS_CHANGED`; hydration had
    // no other way to learn it, because a worker reconnecting changes nothing
    // about the candidate set.
    onlineWorkerIds: () => onlineWorkerIdSet(workerSection.workers()),
    // A worker coming back re-asks for its agent tabs, and that reply must not
    // land over a settings edit the user is making right now.
    settingsPendingAxes: agentId => settingsLoading.pendingAxes(agentId),
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
    repoGitStore,
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    onTurnEnd: handleTurnEnd,
  })

  // Auto-open new workspace dialog from URL search params
  createEffect(() => {
    if (searchParams.newWorkspace === 'true') {
      newWorkspaceDialog.open({
        // A worker and no directory: the URL identifies a machine, not a repository.
        startPoint: { kind: 'directory', workerId: searchParams.workerId as string | undefined },
      })
      setSearchParams({ newWorkspace: undefined, workerId: undefined }, { replace: true })
    }
  })

  // Constant-true today: `(app).tsx` has exactly one leaf (`/`) and
  // hasWorkspaceDesktopChrome matches it, so every guard below always passes.
  // It is kept, not inlined away, because it is the predicate the documented
  // extension path in `(app).tsx` describes: adding a non-workspace leaf under
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
  // signal and persists the new selection — see workspaceSwitcher for why the
  // write has to happen there rather than in an effect. Terminal scrollback is
  // NOT captured here: a workspace switch is only one of the ways a terminal's
  // view goes away, so the capture lives in `disposeTerminalInstance`, the
  // teardown chokepoint every path reaches.
  const switchWorkspace = createWorkspaceSwitcher({
    setActiveWorkspaceId: next => workspace.setActiveWorkspaceId(next),
  })

  // Where a disposing terminal's scrollback lands. Registered once, here,
  // because `TerminalView` owns the xterm instances but must not know about the
  // store graph — the same seam as `setCRDTBridge`. Every teardown path routes
  // through `disposeTerminalInstance`, so a workspace switch, a tab close, a
  // cross-workspace move and a floating-window close all preserve the buffer.
  // `patchExisting`, not `patch`: this fires from `disposeTerminalInstance`, and
  // the view's unmount defers that to a microtask -- so for a terminal closed on
  // another device it runs AFTER the sweep already reclaimed the row. Creating
  // the row again would strand a full serialized scrollback that nothing can
  // ever retire, because the sweep retires an id once and it can never be live
  // again.
  // The sink also rewinds lastOffset to the parsed offset the serialized
  // screen actually covers: the live cursor can sit ahead of the parser (a
  // write is queued before it parses), and a remount would otherwise trim the
  // never-painted span of the next catch-up delta as "already rendered".
  setTerminalScreenSink((tabId, screen, parsedOffset) => tabMetadata.patchExisting(tabId, {
    screen,
    ...(parsedOffset !== undefined ? { lastOffset: parsedOffset } : {}),
  }))
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
      // The `uid` guard stays even though the key no longer carries it:
      // `resolveActiveWorkspace` refuses to act on an empty user id, and a read
      // that ran anyway would throw before the identity resolved.
      savedWorkspaceId: uid ? localStorageGet<string>(KEY_ACTIVE_WORKSPACE) : undefined,
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

  // Whether ONE NAMED workspace can be mutated. Per id, because a dialog can
  // act on a workspace that the user does not look at.
  const isWorkspaceMutatableById = (workspaceId: string): boolean => {
    // An ABSENT workspace -- deleted remotely while a dialog holds its id --
    // is not mutatable. That is the same refusal archival earns, and the same
    // one the caller needs.
    //
    // The presence test lives here, not in `isWorkspaceMutatable`, which takes
    // an archived flag and answers about archival alone. This function holds
    // the id, so only it can test for presence.
    if (!workspaceStore.state.workspaces.some(w => w.id === workspaceId))
      return false
    const sectionId = sectionStore.getSectionForWorkspace(workspaceId)
    const archived = sectionId
      ? sectionStore.state.sections.find(s => s.id === sectionId)?.sectionType === SectionType.WORKSPACES_ARCHIVED
      : false
    return isWorkspaceMutatable(archived)
  }

  /**
   * Whether the desktop shell runs its own bundled sidecar.
   *
   * One resource for the whole SIDEBAR, which mounts a workspace row menu per
   * row and would otherwise ask per menu. Each menu pairs it with the worker's
   * own `autoRegistered` flag through `isLocalWorker`.
   *
   * `OpenInEditorButton` keeps its own resource: it sits outside this tree, it
   * holds no worker, and `localSolo` alone is the whole gate it needs -- see
   * the note there.
   *
   * Asked of the Rust shell rather than derived from the URL: under
   * `task dev-desktop` the webview points at http://localhost:4328, so a URL
   * check misclassifies a solo run as non-solo.
   */
  // The rejection is CAUGHT rather than left to the resource. Solid re-throws a
  // rejected resource from the accessor, `platformBridge` caches the rejected
  // promise for the life of the page, and `localSolo` is read from every
  // workspace row menu -- so one failed IPC call would replace the whole shell
  // with the route's error boundary. Not knowing whether this is a solo desktop
  // means treating it as not one, which hides two menu items.
  const [runtimeState] = createResource(async () =>
    getRuntimeState().catch((err: unknown) => {
      log.warn('get_runtime_state failed; treating this as a non-solo shell', err)
      return undefined
    }),
  )
  const localSolo = () => runtimeState()?.capabilities.localSolo ?? false

  // Whether the active workspace can be mutated.
  //
  // The presence test is HERE and not inside `isWorkspaceMutatable`, which now
  // answers about archival alone. An absent workspace -- deleted remotely while
  // this client still marks it active -- takes no mutation either, and without
  // this test the memo answered `true` for it: no section means not archived.
  // `isWorkspaceMutatableById` makes the same test for the same reason.
  const isActiveWorkspaceMutatable = createMemo(() =>
    !!activeWorkspace() && isWorkspaceMutatable(isActiveWorkspaceArchived()),
  )

  // Active tab derived state
  const activeTab = createMemo(() => {
    const tileId = layoutStore.focusedTileId()
    return tileId ? selection.activeTabForTile(tileId) ?? null : null
  })
  const activeTabType = createMemo(() => activeTab()?.type ?? null)

  // Whether the active tab's working tree is settled enough to query git
  // status. Used to gate every repoGitStore.refresh — both the workspace-list
  // tab fields and the Files section's per-row badges read from the same
  // store, so a single gate protects both. See isTabReadyForGitStatus
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
   * Refresh git file status for the active tab's working tree.
   *
   * Two effects need exactly this — a turn ending, and the active tab's
   * context changing — and each used to hand-write the same three steps 460
   * lines apart. The gate is the subtle part: another agent's turn can fire
   * while the active tab is still inside its phase-0 window, so both must
   * defer until `activeTabReady` (see its definition for why).
   *
   * A context change to a tab with no working tree leaves nothing to refresh;
   * the focused-key effect clears `focusedState()` and readers show empty git UI
   * without wiping other repos in the keyed store.
   */
  const refreshGitStatusForActiveTab = () => {
    if (!activeTabReady())
      return
    const ctx = getCurrentTabContext()
    const tab = activeTab() ?? {}
    const path = gitStatusProbePath(ctx)
    const key = focusedRepoKeyFromTab(tab, ctx, repoGitStore)
    if (ctx.workerId && path)
      void repoGitStore.refresh(ctx.workerId, path, { repoKey: key })
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

  createEffect(() => {
    const tab = activeTab() ?? {}
    const ctx = getCurrentTabContext()
    repoGitStore.setFocusedKey(focusedRepoKeyFromTab(tab, ctx, repoGitStore))
  })

  // Get working directory and home directory from the MRU agent tab
  const getMruAgentContext = (): Pick<TabContext, 'workingDir' | 'homeDir'> => {
    // Straight `.find` on the sorted tabs. Mapping every tab to a `${type}:${id}`
    // key first, matching that on a string prefix, then slicing the id back out
    // to re-look-up the tab allocated one string per tab and a second lookup for
    // the winner -- `mruOrder` already returns the assembled `Tab` objects.
    const agent = mruSteerableAgentTab(tabView.mruOrder(workspace.activeWorkspaceId() ?? ''))
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
    repoGitStore,
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
    repoGitStore,
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
    repoGitStore,
  })
  // Build the final dialog map now that tabOps owns its handle.
  const dialogs: AppShellDialogStates = {
    newAgent: newAgentDialog,
    newTerminal: newTerminalDialog,
    newWorkspace: newWorkspaceDialog,
    confirmDeleteWs: confirmDeleteWsDialog,
    confirmArchiveWs: confirmArchiveWsDialog,
    confirmEmptyArchive: confirmEmptyArchiveDialog,
    sectionName: sectionNameDialog,
    confirmDeleteSection: confirmDeleteSectionDialog,
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
   * Has the bootstrap delivered the workspace on screen? The ONE question
   * the tiling area paints on — and, through `placementTileId`, the one
   * tab placement asks, so the render gate and the create guards agree by
   * construction instead of by coincidence.
   *
   * Asked of the projection rather than tracked in a flag, so nothing can
   * force it true. That distinction is the whole point: a workspace
   * RECORD can arrive without its tree (a split registration batch), and
   * `state.root` then falls back to a locally-minted placeholder leaf.
   * Painting that as real gives its action handlers a node the hub has
   * never heard of. The "open a new agent / terminal" affordances create
   * the worker resource BEFORE emitting the op, so the rejected batch
   * leaves an orphaned agent behind with no tab to reach it by.
   */
  const workspaceReady = createMemo(() => {
    const wsId = workspace.activeWorkspaceId()
    return !!wsId && hasWorkspace(wsId) && layoutStore.hasProjectedTree()
  })

  /**
   * The center's render gate, derived once for both layouts: boolean by
   * construction so a workspace-list refresh that swaps the ACTIVE
   * workspace's object identity (a rename, a lifecycle event) cannot
   * re-run the mobile tile-content insert and remount every pane — the
   * memo's output simply stays `true`.
   */
  const centerReady = createMemo(() => !!activeWorkspace() && workspaceReady())

  /** The shared "nothing to tile" input: no selection AND no saved id. */
  const centerNoWorkspace = createMemo(() => !activeWorkspace() && !workspace.activeWorkspaceId())

  /**
   * Hold BootSplash over the shell until lists and (when applicable) tabs are
   * real. AuthGuard already dropped its splash; without this overlay the
   * chrome paints empty and fills in — see `isShellReady`.
   */
  const shellReady = createMemo(() => isShellReady({
    workspacesLoaded: workspaceStore.state.loaded,
    sectionsLoaded: sectionStore.state.loaded,
    workspaceCount: workspaceStore.state.workspaces.length,
    workspaceError: workspaceStore.state.error,
    activeWorkspaceId: workspace.activeWorkspaceId(),
    centerReady: centerReady(),
    bootstrapTimedOut: bootstrapTimedOut(),
  }))

  // Advance the shell-only checklist rows. Auth already revealed them and set
  // `workspaces`; this moves to `tabs` once both lists finish, then `ready`
  // when the overlay may lift. Login sign-in mounts AppShell with phase still
  // on `ready` from the signed-out bootstrap, so onMount re-enters workspaces.
  onMount(() => {
    setBootShell(true)
    setBootPhase('workspaces')
  })
  onCleanup(() => {
    setBootShell(false)
  })

  // Latch the first time the shell may show. Mid-session `centerReady` dips
  // (workspace switch, CRDT gap) must not remount BootSplash or rewind the
  // checklist — DesktopLayout already gates the blank centre.
  const [shellRevealed, setShellRevealed] = createSignal(false)
  createEffect(() => {
    if (shellReady())
      setShellRevealed(true)
  })
  const shellChromeVisible = createMemo(() => shellRevealed() || shellReady())

  createEffect(() => {
    if (shellReady()) {
      setBootPhase(BOOT_PHASE_READY)
      return
    }
    if (shellRevealed())
      return
    if (workspaceStore.state.loaded && sectionStore.state.loaded)
      setBootPhase('tabs')
  })

  // Keep BootSplash mounted through the exit fade so the ready shell can
  // show underneath (crossfade). Reduced motion skips the wait and drops
  // the overlay in the same tick as shellReady. After the first reveal,
  // never remount the overlay for a later `shellReady` false.
  const [bootSplashMounted, setBootSplashMounted] = createSignal(true)
  createEffect(() => {
    if (!shellReady()) {
      if (!shellRevealed())
        setBootSplashMounted(true)
      return
    }
    if (prefersReducedMotion()) {
      setBootSplashMounted(false)
      return
    }
    const timer = window.setTimeout(setBootSplashMounted, BOOT_SPLASH_ENTER_MS, false)
    onCleanup(() => clearTimeout(timer))
  })

  const openNewWorkspaceDialog = () => {
    newWorkspaceDialog.open({
      targetSectionId: sectionStore.getInProgressSection()?.id ?? null,
    })
  }

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
  // tab, that's the active agent. Todos are owned by the root agent, so a
  // child (subagent) tab resolves up to its root (mirrors background tasks).
  const activeTodos = createMemo(() => {
    const tab = activeTab()
    if (tab?.type !== TabType.AGENT)
      return []
    const rootId = rootAgentIdFor((id: string) => tabView.getAgentTab(id), tab.id)
    return chatStore.todos.get(rootId)
  })

  const showTodos = createMemo(() => activeTabType() === TabType.AGENT && activeTodos().length > 0)

  // Background-task registry for the active agent. Keyed by the ROOT owner
  // agent id, so a child (subagent) tab resolves up to its root and the
  // section stays populated while a child is active. Visible whenever the
  // root has ANY rows (past rows keep the section alive).
  const activeRootAgentId = createMemo(() => {
    const tab = activeTab()
    if (tab?.type !== TabType.AGENT)
      return null
    return rootAgentIdFor((id: string) => tabView.getAgentTab(id), tab.id)
  })
  const activeBackgroundTasks = createMemo(() =>
    activeRootAgentId() ? chatStore.backgroundTasks.get(activeRootAgentId()!) : [],
  )
  // Whether the worker could not answer for this root's registry. Keeps a
  // FAILURE distinguishable from an empty registry: the section is hidden when
  // empty, so without this a worker that cannot read the table takes the whole
  // section off screen with nothing to say why -- which is what a database
  // missing a column did.
  const activeBackgroundTasksFailed = createMemo(() =>
    activeRootAgentId() ? chatStore.backgroundTasks.loadFailed(activeRootAgentId()!) : false,
  )
  const showBackgroundTasks = createMemo(() =>
    shouldShowBackgroundTasksSection(activeBackgroundTasks(), activeBackgroundTasksFailed()),
  )
  // Open a subagent tab from a Background tasks row. Built once here and shared
  // with the sidebar section + the ThinkingIndicator popover (via sidebarOpts).
  const onOpenBackgroundTask = (item: { childAgentId?: string, parentAgentId?: string, title?: string }) => {
    if (!item.childAgentId)
      return
    openSubagentTab(
      {
        view: tabView,
        layoutStore,
        selection,
        metadata: tabMetadata,
        speculativeTabs: () => getCRDTBridge()?.speculativeState()?.tabs ?? {},
        focusTileId: (tileId: string) => focusTile(tileId),
      },
      item,
    )
  }

  // Workspace selection switches in place — there is no per-workspace URL.
  const handleSelectWorkspace = (id: string) => {
    closeMobileOverlay()
    switchWorkspace(id)
  }

  /**
   * Run a new-tab action on a branch's checkout.
   *
   * The tab is placed on the ACTIVE workspace's focused tile, so the branch's
   * own workspace becomes active first. The sidebar lists every workspace's
   * tree, so a branch row often belongs to a workspace the user is not looking
   * at, and without the switch the tab would land in the wrong one.
   *
   * `switchWorkspace` writes one signal, and `layoutStore.placementTileId()`
   * derives from it, so the placement inside `run` already resolves against the
   * new workspace -- there is nothing to await.
   */
  const atStartPoint = (at: WorkspaceStartAt, run: (target: NewTabTarget) => void) => {
    if (at.workspaceId && at.workspaceId !== workspace.activeWorkspaceId())
      handleSelectWorkspace(at.workspaceId)
    run({ workerId: at.workerId, workingDir: at.workingDir })
  }

  /** The branch flavour: a `BranchRef` is one checkout of one workspace. */
  const atBranch = (ref: BranchRef, run: (target: NewTabTarget) => void) =>
    atStartPoint({ workspaceId: ref.workspaceId, workerId: ref.workerId, workingDir: ref.gitToplevel }, run)

  /**
   * The two tab-creation actions a workspace ROW offers, over the same seam as
   * the branch ones above.
   *
   * Both open the DIALOG rather than starting a tab outright: a workspace spans
   * N repositories on up to N workers, so an inline provider or shell list
   * would have to fan out per repository on every menu open.
   */
  const workspaceStartActions: WorkspaceStartActions = {
    onNewAgentAt: at => atStartPoint(at, target => newAgentDialog.open(target)),
    onNewTerminalAt: at => atStartPoint(at, target => newTerminalDialog.open(target)),
  }

  /**
   * Every branch context menu's actions, for both surfaces that render one:
   * the sidebar's per-branch row and the composer's branch chip. Defined once,
   * so an item added to the menu reaches both or neither.
   */
  const branchActions: BranchRefActions = {
    onChangeBranch: openChangeBranchDialog,
    onDeleteBranch: openDeleteBranchDialog,
    onNewAgent: (ref, provider) => atBranch(ref, target => void agentOps.handleOpenAgent(provider, target)),
    onNewAgentAdvanced: ref => atBranch(ref, target => newAgentDialog.open(target)),
    onNewTerminalWithShell: (ref, shell) => atBranch(ref, target => void termOps.handleOpenTerminalWithShell(shell, target)),
    onNewTerminalAdvanced: ref => atBranch(ref, target => newTerminalDialog.open(target)),
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
    // tombstones them, and `useMetadataSweep` reclaims their metadata on the
    // next tick. Only the selection pointer has to be dropped explicitly.
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

  /**
   * The three section-CRUD callbacks, bundled once. See `SectionActions`.
   *
   * A stable object built at setup: the three are plain callbacks over dialog
   * handles, so nothing here needs to stay reactive.
   */
  const sectionActions: SectionActions = {
    onNew: sidebar => sectionNameDialog.open({ mode: 'create', sidebar }),
    onRename: section => sectionNameDialog.open({
      mode: 'rename',
      sectionId: section.id,
      initialName: section.name,
    }),
    onDelete: section => confirmDeleteSectionDialog.open({
      sectionId: section.id,
      sectionName: section.name,
    }),
  }

  const handleConfirmEmptyArchive = (count: number): Promise<boolean> =>
    new Promise((resolve) => {
      confirmEmptyArchiveDialog.open({ count, resolve })
    })

  // Clear transient write state for every agent in the archived workspace.
  const handlePostArchiveWorkspace = (workspaceId: string) => {
    for (const tab of tabView.forWorkspace(workspaceId)) {
      if (tab.type !== TabType.AGENT)
        continue
      controlStore.clearAgent(tab.id)
      chatStore.failPendingOutbound(tab.id, 'Workspace archived before the message was sent')
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
      repoGitStore,
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
      mobileOverlay: mobileOverlayState,
    },
    refs: { focusEditorRef, getScrollStateRef, forceScrollToBottomRef },
    floatingWindow: {
      store: floatingWindowStore,
      onDetachTab: handleDetachTab,
      onAttachTab: handleAttachTab,
    },
    settingsLoading,
    onOpenBackgroundTask,
    onOpenChatImage: tabOps.handleChatImageOpen,
    branch: {
      actions: branchActions,
      isWorkerKnownOnline: workerId => isWorkerKnownOnline(workerSection.workers(), workerId),
    },
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
        toggleMobileDrawer('left')
      }
      else {
        toggleLeftSidebarRef()?.()
      }
    },
    toggleRightSidebar: () => toggleRightSidebarRef()?.(),
    activeTabType,
    resolveFocusedTab: tileRenderer.resolveFocusedTab,
    isActiveWorkspaceArchived,
    splitFocusedTile: tileRenderer.splitFocusedTile,
    scrollFocusedTabPage: tileRenderer.scrollFocusedTabPage,
    writeToFocusedTerminal: tileRenderer.writeToFocusedTerminal,
    getCurrentTabContext,
    customKeybindings: preferences.customKeybindings,
    preferredEditorId: preferences.preferredEditorId,
    setPreferredEditorId: preferences.setPreferredEditorId,
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
    onNewWorkspace: (sectionId: string | null, startPoint: WorkspaceStartPoint) => {
      newWorkspaceDialog.open({ targetSectionId: sectionId, startPoint })
    },
    onRefreshWorkspaces: () => loadWorkspaces(),
    onDeleteWorkspace: handleDeleteWorkspace,
    onConfirmDelete: handleConfirmDeleteWorkspace,
    onConfirmArchive: handleConfirmArchiveWorkspace,
    onConfirmEmptyArchive: handleConfirmEmptyArchive,
    onPostArchiveWorkspace: handlePostArchiveWorkspace,
    sectionActions,
    workspaceStartActions,
    getCurrentTabContext,
    getMruAgentContext,
    get fileTreePath() { return fileTreePath() },
    onFileSelect: setFileTreePath,
    onFileOpen: tabOps.handleFileOpen,
    get isActiveWorkspaceArchived() { return isActiveWorkspaceArchived() },
    get showTodos() { return showTodos() },
    get activeTodos() { return activeTodos() },
    get showBackgroundTasks() { return showBackgroundTasks() },
    get activeBackgroundTasks() { return activeBackgroundTasks() },
    get activeBackgroundTasksFailed() { return activeBackgroundTasksFailed() },
    onOpenBackgroundTask: item => onOpenBackgroundTask(item),
    termOps,
    gitStatusStore: repoGitStore,
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
    get localSolo() { return localSolo() },
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
      onRename: (tab, title) => renameTab({ view: tabView, metadata: tabMetadata }, tab, title),
      get closingKeys() { return tabOps.closingTabKeys() },
    },
    // Tile order for sidebar leaf ordering. No active/inactive fork: every
    // workspace's tree is in the projection. Returns `[]` when no layout is
    // projected yet (the CRDT bootstrap hasn't landed); the tree falls back to
    // a position-only sort.
    getTileOrderForWorkspace: (wsId: string) => layoutStore.tileOrderFor(wsId),
    branchActions,
  })

  // Refresh git status only when workerId or workingDir actually changes
  // (not on every tab switch within the same worker context). Including
  // activeTabReady in the dep key lets the effect re-fire when a STARTING
  // tab finishes its phase-0 window — see activeTabReady for why we defer.
  createEffect(on(
    () => {
      const ctx = getCurrentTabContext()
      return `${ctx.workerId}\0${ctx.gitToplevel}\0${ctx.workingDir}\0${activeTabReady() ? '1' : '0'}`
    },
    () => refreshGitStatusForActiveTab(),
  ))

  // The layer components close over the parent's scope so the outer JSX
  // stays flat — no nested `<Show>` ladders, no need to thread the dozens
  // of in-scope closures (handle*, tile/agent stores, layout store, etc.)
  // through props.
  //
  // The only decision left is desktop vs mobile. The "workspace not found"
  // case went away with the per-workspace URL, and there is deliberately no case
  // for a non-workspace route and no route outlet — see the note on the `(app)`
  // layout, which is where such a case would have to be reintroduced.

  const MobileShellLayer = () => (
    <MobileLayout
      overlay={mobileOverlay()}
      onCloseSheet={closeMobileSheet}
      onSwipe={applyMobileSwipe}
      leftSidebarElement={createLeftSidebarElement(sidebarOpts())}
      rightSidebarElement={createRightSidebarElement(sidebarOpts())}
      tabBarElement={tileRenderer.tabBarElement()}
      // Only while an overlay is not depending on the bar: a drawer and the
      // tab sheet both close through its toggles, and the sheet carries text
      // fields of its own, so the keyboard being up is not sufficient there.
      tabBarHidden={softKeyboardUp() && mobileOverlay() === 'none'}
      tileContent={
        // The same gate the desktop center paints on, for the same reason:
        // `state.root` falls back to a locally-minted placeholder leaf while
        // the projection has no tree, and rendering tile content against it
        // hands its action handlers a node the hub has never heard of. A
        // ternary (not <Show>) so the expression keeps re-deriving on a
        // focusedTileId change — MobileLayout's insert runs this getter in a
        // tracking scope. `centerReady()` is a BOOLEAN memo on purpose: the
        // insert would otherwise track `activeWorkspace()` by object
        // identity, and a list refresh that swaps the active workspace's
        // row would remount every pane mid-use.
        centerReady()
          ? tileRenderer.renderTileContent(layoutStore.focusedTileId())
          : (
              <WorkspaceCenterFallback
                noWorkspace={centerNoWorkspace()}
                bootstrapTimedOut={bootstrapTimedOut()}
                onNewWorkspace={openNewWorkspaceDialog}
              />
            )
      }
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
          layoutStore={layoutStore}
          activeWorkspaceId={workspace.activeWorkspaceId()}
          centerReady={centerReady()}
          centerNoWorkspace={centerNoWorkspace()}
          bootstrapTimedOut={bootstrapTimedOut()}
          getInProgressSectionId={() => sectionStore.getInProgressSection()?.id ?? null}
          onNewWorkspace={openNewWorkspaceDialog}
          setCenterPanelHeight={setCenterPanelHeight}
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
        {/* Both layouts sit in the same drag-provider sandwich: the section
            and tab drag contexts are layout-independent, so the stack lives
            here once instead of being re-declared and re-fed inside each
            layout. */}
        <SectionDragProvider
          sections={() => sectionStore.state.sections}
          onMoveSection={handleMoveSection}
          onMoveSectionServer={handleMoveSectionServer}
        >
          <TabDragProvider
            onIntraTileReorder={tileDrag.handleIntraTileReorder}
            onCrossTileMove={tileDrag.handleCrossTileMove}
            onCrossWorkspaceMove={handleCrossWorkspaceMove}
            lookupTileIdForTab={tileDrag.lookupTileIdForTab}
            renderDragOverlay={tileDrag.renderDragOverlay}
          >
            <Show when={!isMobileLayout()} fallback={<MobileShellLayer />}>
              <DesktopShellLayer />
            </Show>
          </TabDragProvider>
        </SectionDragProvider>
      </div>
    </GridPopoverHostProvider>
  )

  return (
    <TunnelProvider store={workerSection.tunnelStore}>
      {/*
        Rendered unconditionally. There used to be a "workspace not found" case
        here, for a URL naming a workspace the user does not own. No URL identifies
        one any more: the active id is either this user's own click on their own
        sidebar, or a saved id that `resolveActiveWorkspace` has already checked
        against the loaded list — so the dead-end 404 has no way to be reached,
        and the missing-workspace case became a fallback instead of a page.

        BootSplash sits as an absolute overlay until `shellReady`, then fades
        out over the revealed chrome (exit crossfade). The shell mounts
        underneath (visibility:hidden) so list/CRDT work continues; it becomes
        visible the moment the fade starts.
      */}
      <div class={styles.bootShellHost}>
        <div
          class={styles.bootShellContent}
          data-ready={shellChromeVisible() ? '' : undefined}
          aria-hidden={shellChromeVisible() ? undefined : true}
        >
          <WorkspaceShellLayer />
        </div>

        {/*
          Dialogs sit outside the visibility-hidden shell chrome so a CLI path
          prompt (or any other dialog) that opens during the boot hold stays
          visible and usable on top of the splash.
        */}
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
          loadSections={loadSections}
          onBranchChanged={(repo, newBranch) => handleBranchChanged(
            { repoGitStore },
            repo,
            newBranch,
          )}
          activeWorkspace={activeWorkspace}
          isWorkspaceMutatable={isWorkspaceMutatableById}
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
          repoGitStore={repoGitStore}
        />

        <workerSection.Dialogs />

        <Show when={bootSplashMounted()}>
          <div
            class={styles.bootSplashOverlay}
            data-exiting={shellReady() ? '' : undefined}
            aria-hidden={shellReady() || undefined}
          >
            <BootSplash />
          </div>
        </Show>
      </div>
    </TunnelProvider>
  )
}
