import type { ParentComponent } from 'solid-js'
import type { AppShellDialogStates, ChangeBranchState, DeleteBranchState, KeyPinConfirmState, NewWorkspacePayload, WorkspaceConfirmPayload } from './AppShellDialogs'
import type { SidebarElementsOpts } from './SidebarElements'
import type { TabContext } from './tabContext'
import type { BatchOutcome } from './useOpsSubmitter'
import type { CliPathStatus } from '~/api/platformBridge'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import type { GetGitFileStatusResponse } from '~/generated/leapmux/v1/git_pb'
import type { Worker } from '~/generated/leapmux/v1/worker_pb'
import type { Tab } from '~/stores/tab.types'
import { useLocation, useNavigate, useParams, useSearchParams } from '@solidjs/router'
import { createEffect, createMemo, createSignal, Match, on, Show, Switch, untrack } from 'solid-js'
import { workerClient } from '~/api/clients'
import { isTauriApp, platformBridge } from '~/api/platformBridge'
import { apiLoadingTimeoutMs } from '~/api/transport'
import { channelManager, getGitFileStatus, renameAgent, setConfirmKeyPin, setGetUserId } from '~/api/workerRpc'
import { NotFoundPage } from '~/components/common/NotFoundPage'
import { showWarnToast } from '~/components/common/Toast'
import { CliPathDialog } from '~/components/desktop/CliPathDialog'
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
import { createDialogState, createToggleDialog } from '~/hooks/createDialogState'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { useChatAutoFocus } from '~/hooks/useChatAutoFocus'
import { useIsMobileLayout } from '~/hooks/useIsMobileLayout'
import { useShortcuts } from '~/hooks/useShortcuts'
import { useVisualViewportInset } from '~/hooks/useVisualViewportInset'
import { useWorkspaceConnection } from '~/hooks/useWorkspaceConnection'
import { KEY_ACTIVE_WORKSPACE, KEY_CLI_PATH_CHECKED, KEY_CLIENT_ID, sessionStorageGet, sessionStorageSet } from '~/lib/browserStorage'
import { HLCClock, PendingOpsManager, projectWorkspaceTabs, setCRDTBridge } from '~/lib/crdt'
import { hlcIsZero } from '~/lib/crdt/hlc'
import { hasWorkspaceDesktopChrome } from '~/lib/desktopChrome'
import { createFileTabPathsStore } from '~/lib/fileTabPaths'
import { createIdentityCache } from '~/lib/identityCache'
import { randomUUID } from '~/lib/idGenerator'
import { createImperativeRef } from '~/lib/imperativeRef'
import { createLogger } from '~/lib/logger'
import { setDashboardTitle, setWorkspaceTitle } from '~/lib/pageTitle'
import { parentDirectory } from '~/lib/paths'
import { createActiveClientStore } from '~/lib/presence/activeClient'
import { mountPresenceHeartbeat } from '~/lib/presence/heartbeat'
import { isMac } from '~/lib/shortcuts/platform'
import { printConsoleBanner } from '~/lib/systemInfo'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import { createGitFileStatusStore } from '~/stores/gitFileStatus.store'
import { createLayoutStore, getAllTileIds } from '~/stores/layout.store'
import { createSectionStore } from '~/stores/section.store'
import { agentTabToInfo, isSameRepo, isTabReadyForGitStatus, tabKey } from '~/stores/tab.helpers'
import { createTabStore } from '~/stores/tab.store'
import { createTunnelStore } from '~/stores/tunnel.store'
import { createWorkerChannelStatusStore } from '~/stores/workerChannelStatus.store'
import { workerInfoStore } from '~/stores/workerInfo.store'
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
import { applyGitStatusToTabs, syncGitStatusToTabs, tabStoreTarget } from './syncGitStatusToTabs'
import { focusTile as focusTileShared } from './tileLifecycle'
import { createTileRenderer } from './TileRenderer'
import { useAgentOperations } from './useAgentOperations'
import { useCrossWorkspaceMove } from './useCrossWorkspaceMove'
import { useFloatingWindowOps } from './useFloatingWindowOps'
import { useFocusInvariant } from './useFocusInvariant'
import { createOpsSubmitter } from './useOpsSubmitter'
import { useOrgEvents } from './useOrgEvents'
import { useTabHydrators } from './useTabHydrators'
import { useTabOperations } from './useTabOperations'
import { useTabPersistence } from './useTabPersistence'
import { useTerminalOperations } from './useTerminalOperations'
import { useTileDragDrop } from './useTileDragDrop'
import { useTurnEnd } from './useTurnEnd'
import { useWorkerPrivateStreams } from './useWorkerPrivateStreams'
import { useWorkspaceHydration } from './useWorkspaceHydration'
import { useWorkspaceLoader } from './useWorkspaceLoader'
import { useWorkspaceRestore } from './useWorkspaceRestore'
import { useWorkspaceSwitchSnapshot } from './useWorkspaceSwitchSnapshot'

// Stable empty-array reference for the inactive-workspace tile-order
// path. Returning a fresh `[]` per call would defeat the WeakMap-based
// memoization that keeps `WorkspaceTabTree`'s `buildTree` memo stable.
const EMPTY_TILE_ORDER: string[] = []

const log = createLogger('AppShell')

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
  const chatStore = createChatStore()
  const tabStore = createTabStore()
  const controlStore = createControlStore()
  const agentSessionStore = createAgentSessionStore()
  const layoutStore = createLayoutStore()
  const floatingWindowStore = createFloatingWindowStore()
  useFocusInvariant({ layoutStore, floatingWindowStore })
  const gitFileStatusStore = createGitFileStatusStore()
  const [fileTreePath, setFileTreePath] = createSignal('')
  const [workspaceLoading, setWorkspaceLoading] = createSignal(true)
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

  // Worker section state
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
      keyPinConfirmDialog.open({ workerId, expectedFingerprint, actualFingerprint, resolve })
    }),
  )
  setGetUserId(() => auth.user()?.id ?? '')

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

  // Per-(workspace_id) active-client tracker fed by PresenceUpdate
  // events on OrgCRDT.WatchOrg. AppShell.handleTurnEnd consults this
  // to gate the turn-end ding so only the focused client plays it.
  const activeClient = createActiveClientStore()
  // Cache of file-tab paths fed by WatchWorkspacePrivateEvents
  // bootstrap replay + GetFileTabPath fallback. Components consult
  // this for FILE-tab titles instead of asking the hub.
  const fileTabPaths = createFileTabPathsStore()

  // Stable client_id for this browser session. Used as the HLC author
  // for op-stamping and as the `op_id` salt — the local random nanoid
  // gives every pending op a deterministic identity for echo dedup.
  //
  // **The active-client gate does NOT compare against this.** The hub
  // identifies subscribers by session id / bearer-token id (so it can
  // refuse cross-tab spoofing), which never matches the local nanoid.
  // The hub returns the subscriber's effective identity in
  // `OrgMaterialized.subscriber_client_id`; the gate compares
  // `activeClient.activeFor(wsId)` against `effectiveClientId()`.
  const ownClientId = (() => {
    const cur = sessionStorageGet<string>(KEY_CLIENT_ID)
    if (cur)
      return cur
    const fresh = `c-${randomUUID()}`
    sessionStorageSet(KEY_CLIENT_ID, fresh)
    return fresh
  })()

  // Effective identity reported by the hub via OrgMaterialized; the
  // active-client gate compares broadcast `active_client_id` against
  // this. Empty until bootstrap; gate treats empty as "unknown — allow"
  // so a sole client plays its ding even before the first heartbeat
  // broadcast settles.
  const [effectiveClientId, setEffectiveClientId] = createSignal('')
  // Local CRDT pending manager + clock. Constructed lazily once the
  // org id is known; useOrgEvents seeds the bootstrap from
  // OrgMaterialized and useOpsSubmitter drives commits.
  const [pendingMgr, setPendingMgr] = createSignal<PendingOpsManager | null>(null)
  // Reactive version counter that bumps on every PendingOpsManager
  // state mutation (submit / consumeRemote / consumeBatch* /
  // consumeEntity* / bootstrap). Subscribed by `bridge.speculativeState`
  // so memoized projections in the layout / floating-window / tab
  // stores re-derive when ops land. The PendingOpsManager mutates
  // its OrgCrdtState in place; this signal is how Solid observes it.
  const [pendingVersion, setPendingVersion] = createSignal(0)
  const bumpPending = () => setPendingVersion(v => v + 1)
  createEffect(() => {
    const oid = org.orgId()
    if (!oid) {
      setPendingMgr(null)
      return
    }
    setPendingMgr(new PendingOpsManager(oid, new HLCClock(ownClientId), bumpPending))
  })

  // Late-bound reload trigger for workspace-lifecycle events. Bound
  // once `useWorkspaceLoader` is instantiated later in this component;
  // until then, lifecycle events arriving before mount complete are a
  // no-op (the initial `listWorkspaces` runs once `getOrgId()` fires,
  // so we don't miss the seed list).
  let reloadWorkspacesOnLifecycle: () => void = () => {}

  // Hook for batch-result callbacks (cross-workspace move rollback,
  // etc.). Populated by the submitter and consulted by AppShell-level
  // handlers that need to react to specific batch ids.
  const batchResultHandlers = new Map<string, (outcome: BatchOutcome) => void>()

  // Open the per-org `/ws/orgevents` subscription once the org id is
  // known. The hook stays live across workspace switches; per-
  // workspace stores slice the materialized state instead.
  const orgEvents = useOrgEvents({
    orgId: () => org.orgId(),
    activeClient,
    pending: () => pendingMgr(),
    onWorkspaceLifecycleChanged: () => reloadWorkspacesOnLifecycle(),
    onSubscriberClientId: id => setEffectiveClientId(id),
    onPendingDropped: () => {
      // EntityRemoved dropped at least one pending op (a redacted
      // entity left the visible set with a local mutation still in
      // flight). Surface a warn-toast so the user understands their
      // recent action didn't take effect.
      showWarnToast('A pending change was discarded because the affected item left your view.')
    },
  })

  // 16ms aggregator for op submission. Stores call into the CRDT
  // bridge below; the submitter handles the SubmitOps RPC + per-
  // batch commit/reject result dispatch, including:
  //   - epoch_required → reconnect + retry (refresh currentEpoch)
  //   - stale_epoch → reconnect + warn-toast (no auto-retry)
  //   - any other rejection → drop + warn-toast keyed by reason
  //   - transport timeout → retry same op_ids (principal-aware dedup)
  const opsSubmitter = createOpsSubmitter({
    orgId: () => org.orgId(),
    pending: () => pendingMgr(),
    reconnect: () => orgEvents.reconnect(),
    onBatchResult: (batchId, outcome) => {
      const cb = batchResultHandlers.get(batchId)
      if (cb)
        cb(outcome)
    },
  })

  // Wire the global CRDT bridge so the imperative stores can emit op
  // batches without threading every dependency through their
  // constructors. Re-installed on every reactive change to the
  // pending manager / org id.
  createEffect(() => {
    const mgr = pendingMgr()
    const oid = org.orgId()
    if (!mgr || !oid) {
      setCRDTBridge(null)
      return
    }
    setCRDTBridge({
      orgId: () => oid,
      workspaceId: () => workspace.activeWorkspaceId() ?? null,
      enqueue: (batch) => {
        opsSubmitter.enqueue(batch)
        return batch.batchId
      },
      clock: () => mgr.clock,
      originClientId: () => ownClientId,
      speculativeState: () => {
        // Read the version signal so memoized consumers re-derive on
        // every mutation. The manager updates its state in place; the
        // version bump is the only Solid-observable signal.
        pendingVersion()
        return mgr.state.speculativeState
      },
    })
  })

  // Snapshot the OUTGOING workspace's tabStore into the registry the
  // moment activeWorkspaceId changes — BEFORE the reconciler effect
  // below mutates tabStore.state.tabs to the new workspace's tabs.
  // Without this, the reconciler runs first (it's registered earlier
  // than useWorkspaceRestore), wipes the previous workspace's tabs
  // from tabStore, and useWorkspaceRestore's snapshot-save then
  // captures the NEW workspace's tabs as the OUTGOING workspace's
  // cached state — corrupting every subsequent workspace-switch-back.
  // (Snapshot-outgoing for the previous workspace happens inside the
  // URL → activeWorkspaceId sync effect below, so it fires SYNCHRONOUSLY
  // before any other effect observes the activeWorkspaceId change. This
  // matters because Solid's effect-scheduling order for downstream
  // signal-dependents isn't strictly "earlier-registered first" in
  // practice — useWorkspaceRestore's createEffect was firing first and
  // calling tabStore.clear() before the snapshot effect could read the
  // outgoing workspace's tabs.)

  // Reconcile the local `tab.store.tabs` against the CRDT projection
  // every time the speculative state changes (op echo, remote op,
  // bootstrap). The reconciler:
  //   - drops tabs the projection no longer renders in the active
  //     workspace (e.g. cross-workspace move, remote tombstone);
  //   - adds tabs the projection has but the local store doesn't
  //     yet (e.g. another client opened an agent / file tab);
  //   - syncs CRDT-driven fields (tile_id, position, worker_id) when
  //     they diverge.
  // All mutations are silent so the reconciler doesn't re-emit ops
  // it just absorbed.
  createEffect(() => {
    // Track ONLY the signals that should re-run reconciliation:
    // pendingVersion (CRDT op applied), pendingMgr (bridge wired),
    // activeWorkspaceId (workspace switch). Without `untrack` around
    // the body, the `state.tabs` reads inside `reconcileFromProjection`
    // subscribe this effect to tabStore mutations — so an optimistic
    // `tabStore.addTab` (cross-workspace move, file open, etc.)
    // immediately re-runs the reconciler against a CRDT projection
    // that hasn't been updated yet. Step 1 then removes the
    // optimistic tab as "gone from this workspace", and when the
    // canonical move op finally lands, step 2 re-adds it as a bare
    // tab (no title / agentProvider / git fields). Reproduces as a
    // cross-workspace dragged tab showing its nanoid + generic icon
    // until the user refreshes the page.
    pendingVersion()
    const mgr = pendingMgr()
    const wsId = workspace.activeWorkspaceId()
    if (!mgr || !wsId)
      return
    untrack(() => {
      const state = mgr.state.speculativeState
      if (!state)
        return
      const renderedTabs = projectWorkspaceTabs(state, wsId)
      const knownIds = new Set(Object.keys(state.tabs))
      // Identify tabs whose CRDT record is alive but whose tile_id
      // currently points at a node we haven't installed locally yet.
      // The hub strips newly-created node ops out of the Batch frame
      // (they're pre-invisible / post-visible) and ships them as
      // separate EntityMaterialized frames; between the Batch frame
      // landing and the EntityMaterialized frames landing, the tab's
      // tileId is a dead-end pointer. Telling the reconciler about
      // these lets it keep the tab in the local store instead of
      // dropping then re-adding it as a bare CRDT-only row (which
      // throws away worker-supplied title / agent metadata and re-
      // sorts the tabstrip by tab_id rather than position).
      const transientUnresolvableTabIds = new Set<string>()
      for (const t of Object.values(state.tabs)) {
        if (!hlcIsZero(t.tombstoneAt))
          continue
        const tileId = t.tileId?.value ?? ''
        if (tileId === '' || !state.nodes[tileId])
          transientUnresolvableTabIds.add(t.tabId)
      }
      tabStore.reconcileFromProjection({
        workspaceId: wsId,
        renderedTabs,
        crdtKnownTabIds: knownIds,
        transientUnresolvableTabIds,
      })
    })
  })

  // Hydrate CRDT-projected tabs whose worker-side metadata (file path,
  // agent record, terminal title) hasn't arrived yet. The hook fires
  // off one best-effort fetch per pending tab; see useTabHydrators for
  // the per-type predicates.
  useTabHydrators({
    tabStore,
    fileTabPaths,
    getOrgId: () => org.orgId(),
  })

  // Mount the input-driven heartbeat for the active workspace. The
  // returned `pingNow` is wired below to fire whenever the
  // `/ws/orgevents` subscription completes its bootstrap — the hub's
  // PresenceUpdate broadcast only reaches subscribers, so a
  // heartbeat sent before the WS is connected never makes it back to
  // this client and the active-client gate stays empty.
  const heartbeat = mountPresenceHeartbeat({
    orgId: () => org.orgId(),
    workspaceId: () => workspace.activeWorkspaceId() ?? '',
  })

  // Fire an immediate heartbeat on every stream (re)connect so the
  // hub's `received_at` is fresh against a subscription that can
  // actually receive the resulting broadcast. Tracks the
  // `bootstrapped` flip false → true.
  let lastBootstrapped = false
  createEffect(() => {
    const now = orgEvents.bootstrapped()
    if (now && !lastBootstrapped)
      heartbeat.pingNow()
    lastBootstrapped = now
  })

  // One WatchWorkspacePrivateEvents subscription per worker hosting a
  // tab in the active workspace. Drives the file-tab path cache and
  // mirrors rename / register / revoke events into the local stores.
  // See useWorkerPrivateStreams for the per-worker open/close logic.
  useWorkerPrivateStreams({
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    tabStore,
    fileTabPaths,
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
    tabStore,
    controlStore,
    agentSessionStore,
    registry,
    settingsLoading,
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    getWorkerId: () => {
      const tileId = layoutStore.focusedTileId()
      const tab = tileId ? tabStore.getActiveTabForTile(tileId) : null
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

  // Sync workspaceId from URL params to WorkspaceContext, snapshotting
  // the outgoing workspace's stores into the registry BEFORE the
  // activeWorkspaceId signal flips. See useWorkspaceSwitchSnapshot for
  // why the snapshot must run before the downstream restore effects.
  useWorkspaceSwitchSnapshot({
    getURLWorkspaceId: () => params.workspaceId,
    tabStore,
    layoutStore,
    registry,
    setActiveWorkspaceId: next => workspace.setActiveWorkspaceId(next),
  })

  // Workspace & section loading
  const { loadWorkspaces, loadSections, handleMoveSection, handleMoveSectionServer } = useWorkspaceLoader({
    getOrgId: () => org.orgId(),
    workspaceStore,
    sectionStore,
  })
  // Now that loadWorkspaces is in scope, wire it to the lifecycle
  // event callback above. Created/renamed/deleted events on the org
  // WS stream will refresh the sidebar without requiring a reconnect.
  reloadWorkspacesOnLifecycle = () => {
    void loadWorkspaces()
    void loadSections()
  }

  // Auto-activate workspace when navigating to org root with no workspace selected
  createEffect(() => {
    if (!isWorkspaceRoute())
      return
    if (params.workspaceId)
      return
    const workspaces = workspaceStore.state.workspaces
    if (workspaces.length === 0)
      return
    // Skip while the NewWorkspaceDialog is open. The dialog races a
    // multi-step async flow (CreateWorkspace → openAgent → seedTab →
    // registry pre-seed) against the WorkspaceCreated event the hub
    // broadcasts inline during CreateWorkspace; the event triggers
    // workspaceStore refresh which would otherwise fire this effect
    // and navigate to the new workspace BEFORE the dialog finishes
    // its pre-seed. `useWorkspaceRestore` then runs with no cached
    // snapshot, `tabStore.clear()`s, and the dialog's later pre-seed
    // lands on already-emptied stores. The freshly-opened agent ends
    // up rendered as a bare CRDT-projection tab (raw id, "Agent not
    // found").
    if (newWorkspaceDialog.value())
      return
    const savedId = sessionStorageGet<string>(KEY_ACTIVE_WORKSPACE)
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

  syncGitStatusToTabs({ gitFileStatusStore, tabStore })

  // Get working directory and home directory from the MRU agent tab
  const getMruAgentContext = (): Pick<TabContext, 'workingDir' | 'homeDir'> => {
    const agentPrefix = `${TabType.AGENT}:`
    const mruKey = tabStore.findMruMatching(k => k.startsWith(agentPrefix))
    if (!mruKey)
      return { workingDir: '', homeDir: '' }
    const agentId = mruKey.slice(agentPrefix.length)
    const agent = tabStore.getAgentTab(agentId)
    return {
      workingDir: agent?.workingDir ?? '',
      homeDir: workerInfoStore.getHomeDir(agent?.workerId ?? ''),
    }
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

  const agentOps = useAgentOperations({
    agentSessionStore,
    chatStore,
    controlStore,
    tabStore,
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
    org,
    tabStore,
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
    tabStore,
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
    getOrgId: () => org.orgId(),
    getActiveWorkspaceId: () => workspace.activeWorkspaceId() ?? undefined,
    registry,
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

  // Lazy-load tabs + agent/terminal metadata for a non-active
  // workspace when its sidebar tree is expanded. The hook owns the
  // inflight dedup (so two sidebar instances firing the same expand
  // effect coalesce) and writes the full snapshot to the registry on
  // success. Declared before useWorkspaceRestore so the restore path
  // can fire it for sibling workspaces in the same microtask as the
  // active workspace's ListTabs — the batcher coalesces them.
  const { expand: handleExpandWorkspace } = useWorkspaceHydration({
    registry,
    getOrgId: () => org.orgId(),
  })

  // Workspace restore (load agents/terminals/tabs/layout on workspace change)
  useWorkspaceRestore({
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    getOrgId: () => org.orgId(),
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

  // Mirror tabStore / layoutStore view-state to sessionStorage so the
  // restore path above can re-activate the user's last tab / tile after
  // a refresh. Gated by `workspaceLoading` so the restore's `clear()`
  // can't blow away the keys mid-flight.
  useTabPersistence({
    tabStore,
    layoutStore,
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    workspaceLoading,
  })

  // Tile drag-and-drop
  const tileDrag = useTileDragDrop({ tabStore, layoutStore, floatingWindowStore })

  const focusTile = (tileId: string) => focusTileShared(layoutStore, floatingWindowStore, tileId)

  // --- Floating window tab movement operations ---
  const { handleDetachTab, handleAttachTab, handleToggleFloatingTab, handleActivateFloatingWindow }
    = useFloatingWindowOps({ layoutStore, floatingWindowStore, tabStore })

  const { move: handleCrossWorkspaceMove } = useCrossWorkspaceMove({
    getActiveWorkspaceId: () => workspace.activeWorkspaceId(),
    getOrgId: () => org.orgId(),
    tabStore,
    layoutStore,
    floatingWindowStore,
    registry,
    pendingMgr,
    batchResultHandlers,
    focusTile,
  })

  // Active agent todos (for right sidebar To-dos pane). "Active" is
  // derived from the active tab — if the user is looking at an AGENT
  // tab, that's the active agent.
  const activeTodos = createMemo(() => {
    const tab = activeTab()
    if (tab?.type !== TabType.AGENT)
      return []
    return chatStore.getTodos(tab.id)
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
      confirmDeleteWsDialog.open({ workspaceId, resolve })
    })

  const handleConfirmArchiveWorkspace = (workspaceId: string): Promise<boolean> =>
    new Promise((resolve) => {
      confirmArchiveWsDialog.open({ workspaceId, resolve })
    })

  // Post-archive cleanup
  const handlePostArchiveWorkspace = (workspaceId: string) => {
    if (workspace.activeWorkspaceId() === workspaceId) {
      for (const tab of tabStore.state.tabs) {
        if (tab.type === TabType.AGENT)
          controlStore.clearAgent(tab.id)
      }
    }
  }

  // Tile renderer (tab bars, tile content, editor panel)
  const tileRenderer = createTileRenderer({
    stores: {
      tabStore,
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
    tabStore,
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

  // Per-`layout.root` cache for inactive-workspace tile orders. Keyed by
  // the layout root's object identity so each `getTileOrderForWorkspace`
  // call returns the same array reference as long as the registry's
  // snapshot is unchanged. Without this cache, every call would allocate
  // a fresh array via `getAllTileIds(root).flatMap(...)`, invalidating
  // `WorkspaceTabTree`'s `buildTree` memo on each tick.
  const inactiveTileOrderCache = new WeakMap<object, string[]>()

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
          const workerId = tabStore.getAgentTab(tab.id)?.workerId ?? ''
          renameAgent(workerId, { agentId: tab.id, title }).catch((err) => {
            showWarnToast('Failed to rename agent', err)
          })
        }
      },
      get closingKeys() { return tabOps.closingTabKeys() },
    },
    onExpandWorkspace: handleExpandWorkspace,
    // Tile order for sidebar leaf ordering. The active workspace's
    // layout lives on `layoutStore` (the registry snapshot is taken at
    // workspace-switch time, so it's stale for the active workspace);
    // other workspaces read from the snapshot. The active path uses
    // the store's memoized accessor so the WorkspaceTabTree memo
    // downstream isn't invalidated by a fresh array on every unrelated
    // reactive tick. The inactive path caches by `root` reference so
    // repeated calls return the same array identity for as long as the
    // registry snapshot's layout tree is unchanged — without that, a
    // fresh `flatMap`-allocated array would invalidate the downstream
    // `buildTree` memo on every render tick. Returns `[]` when no
    // layout is loaded yet (cold registry); the tree falls back to a
    // position-only sort.
    getTileOrderForWorkspace: (wsId: string) => {
      if (wsId === workspace.activeWorkspaceId())
        return layoutStore.getAllTileIds()
      const root = registry.get(wsId)?.layout.root
      if (!root)
        return EMPTY_TILE_ORDER
      const cached = inactiveTileOrderCache.get(root)
      if (cached)
        return cached
      const fresh = getAllTileIds(root)
      inactiveTileOrderCache.set(root, fresh)
      return fresh
    },
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

  // Three layer components close over the parent's scope so the outer
  // JSX is a flat `<Switch>` — no nested `<Show>` ladders, no need to
  // thread the dozens of in-scope closures (handle*, tile/agent
  // stores, layout store, etc.) through props.
  //
  // The 3-way decision: workspaceNotFound → NotFoundPage; on a
  // workspace route → the workspace shell (desktop or mobile); else →
  // fullWindow children (dashboard layout serving as the route's
  // wrapper).

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
          workspaceLoading={workspaceLoading()}
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
              tabStore={tabStore}
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
    <TunnelProvider store={tunnelStore}>
      <Switch>
        <Match when={workspaceNotFound()}>
          <NotFoundPage
            message="The workspace you're looking for doesn't exist or you don't have access."
            linkHref={`/o/${params.orgSlug}`}
            linkText="Go to Dashboard"
          />
        </Match>
        <Match when={isWorkspaceRoute()}>
          <WorkspaceShellLayer />
        </Match>
        <Match when={true}>
          <div class={styles.fullWindow}>{props.children}</div>
        </Match>
      </Switch>

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
        onBranchChanged={(workerId, workingDir, newBranch) => {
          // Immediate branch-label stamp on every tab in
          // (workerId, gitToplevel): the active workspace's tabStore
          // directly, every inactive workspace's registry snapshot
          // indirectly. Without the registry fan-out, a Change branch
          // opened on an inactive workspace's sidebar row would update
          // the active workspace's matching tabs (rare, but possible
          // when the same worker hosts both repos) yet leave the
          // INACTIVE workspace's branch label stale until next switch.
          tabStore.stampBranchOnTabs(workerId, workingDir, newBranch)
          const activeWsId = activeWorkspace()?.id
          for (const snap of registry.all()) {
            if (snap.workspaceId === activeWsId)
              continue
            // No-op when no tab in the snapshot matches — keeps the
            // registry version counter from churning every reactive
            // consumer for an empty pass.
            if (!snap.tabs.some(t => isSameRepo(t, workerId, workingDir) && t.gitBranch !== newBranch))
              continue
            registry.update(snap.workspaceId, s => ({
              ...s,
              tabs: s.tabs.map(t =>
                isSameRepo(t, workerId, workingDir) && t.gitBranch !== newBranch
                  ? { ...t, gitBranch: newBranch }
                  : t,
              ),
            }))
          }
          // Diff-stats refresh. Active repo: refresh the file-status
          // singleton so the file tree updates and syncGitStatusToTabs
          // cascades into the active tabStore. Inactive repo: fetch
          // directly and stamp tabs across active tabStore + every
          // inactive registry snapshot, but don't touch the singleton
          // (it tracks the focused repo's file tree, and a non-focused
          // refresh would flip the tree view to a repo the user isn't
          // looking at).
          //
          // ALWAYS stamp inactive workspaces' diff stats too — that's
          // the only way an inactive workspace's sidebar diff badges
          // can pick up post-branch-change state without waiting for
          // its switch-in refresh.
          const stampInactiveFromStatus = (status: {
            repoRoot: string
            toplevel: string
            originUrl: string
            currentBranch: string
            files: GetGitFileStatusResponse['files']
          }) => {
            for (const snap of registry.all()) {
              if (snap.workspaceId === activeWsId)
                continue
              applyGitStatusToTabs(
                {
                  tabs: snap.tabs,
                  update: (predicate, fields) => registry.update(snap.workspaceId, s => ({
                    ...s,
                    tabs: s.tabs.map(t => predicate(t) ? { ...t, ...fields } as Tab : t),
                  })),
                },
                status,
              )
            }
          }
          if (isSameRepo(getCurrentTabContext(), workerId, workingDir)) {
            void gitFileStatusStore.refresh(workerId, workingDir)
              .then(() => {
                // Reuse the singleton's freshly-refreshed state for the
                // inactive fan-out rather than firing a second getGitFileStatus.
                stampInactiveFromStatus({
                  repoRoot: gitFileStatusStore.state.repoRoot,
                  toplevel: gitFileStatusStore.state.toplevel,
                  originUrl: gitFileStatusStore.state.originUrl,
                  currentBranch: gitFileStatusStore.state.currentBranch,
                  files: gitFileStatusStore.state.files,
                })
              })
          }
          else {
            void getGitFileStatus(workerId, { workerId, path: workingDir })
              .then((resp) => {
                // Worker fallback: pre-toplevel builds (or response-shape
                // regressions) leave toplevel empty; treat repoRoot as
                // toplevel so the non-worktree case keeps working. Once
                // the worker reliably ships toplevel, this fallback is
                // dead code.
                const status = {
                  repoRoot: resp.repoRoot,
                  toplevel: resp.toplevel || resp.repoRoot,
                  originUrl: resp.originUrl,
                  currentBranch: resp.currentBranch,
                  files: resp.files,
                }
                applyGitStatusToTabs(tabStoreTarget(tabStore), status)
                stampInactiveFromStatus(status)
              })
              .catch((err) => {
                log.warn('failed to refresh git status for non-active repo', err)
              })
          }
        }}
        activeWorkspace={activeWorkspace}
        getCurrentTabContext={getCurrentTabContext}
        agentOps={agentOps}
        termOps={termOps}
        tabOps={tabOps}
        tabStore={tabStore}
        layoutStore={layoutStore}
        sectionStore={sectionStore}
        registry={registry}
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
