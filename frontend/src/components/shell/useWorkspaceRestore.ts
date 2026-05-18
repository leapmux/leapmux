import type { createAgentSessionStore } from '~/stores/agentSession.store'
import type { createChatStore } from '~/stores/chat.store'
import type { createControlStore } from '~/stores/control.store'
import type { FloatingWindowStoreType } from '~/stores/floatingWindow.store'
import type { createLayoutStore } from '~/stores/layout.store'
import type { createTabStore } from '~/stores/tab.store'
import type { Tab } from '~/stores/tab.types'
import type { WorkspaceStoreRegistryType } from '~/stores/workspaceStoreRegistry'
import { batch, createEffect, createMemo, createRoot, createSignal, on, onCleanup } from 'solid-js'
import { listTabsForWorkspace } from '~/api/listTabsBatcher'
import * as workerRpc from '~/api/workerRpc'
import { readExpandedWorkspaceIds } from '~/components/workspace/expandedWorkspaces'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { sessionStorageGet } from '~/lib/browserStorage'
import { getCRDTBridge } from '~/lib/crdt/bridge'
import { hlcIsZero } from '~/lib/crdt/hlc'
import { createInflightCache } from '~/lib/inflightCache'
import { createLogger } from '~/lib/logger'
import { createExponentialBackoff } from '~/lib/retry'
import { agentTabToInfo, parseTabKey, preserveNonEmptyGitFields, preserveTerminalDisplayFields, protoToAgentTabFields, protoToTerminalTab, protoToTerminalTabFields, tabKey, tabsByKey } from '~/stores/tab.helpers'
import { activeTabKey, focusedTileKey, tileActiveTabsKey } from './tabPersistenceKeys'
import { fanOutTabsToWorkers } from './workspaceTabHydration'

const log = createLogger('restore')

/**
 * Resolve once the OrgCRDT bootstrap has delivered the given workspace's
 * root NodeRecord into `speculativeState`. Without this gate the UI would
 * flip to the layout-store's placeholder fallback (`FALLBACK_LEAF`) before
 * the WS round-trip lands and the user (or an E2E test) could click on
 * action affordances bound to a tile id the hub doesn't know about.
 *
 * Rejects if the workspace generation rolls over (the active workspace
 * changed before bootstrap landed) so callers can abandon the wait.
 */
function awaitWorkspaceBootstrap(
  workspaceId: string,
  isCurrent: () => boolean,
): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    let settled = false
    createRoot((dispose) => {
      createEffect(() => {
        if (settled)
          return
        if (!isCurrent()) {
          settled = true
          dispose()
          reject(new Error('workspace bootstrap superseded'))
          return
        }
        const bridge = getCRDTBridge()
        if (!bridge)
          return
        const state = bridge.speculativeState()
        if (!state)
          return
        const ws = state.workspaces[workspaceId]
        if (!ws)
          return
        if (ws.rootNodeId === '')
          return
        const root = state.nodes[ws.rootNodeId]
        if (!root)
          return
        if (!hlcIsZero(root.tombstoneAt))
          return
        settled = true
        dispose()
        resolve()
      })
    })
  })
}

/**
 * Shared context for the restore phases. Bundles the per-pass derived
 * collections so each phase has one parameter to pass instead of six.
 */
interface RestoreCtx {
  tabStore: ReturnType<typeof createTabStore>
  defaultTileId: string | undefined
  validTileIds: Set<string>
  tabTileMap: Map<string, string>
  cachedByKey: Map<string, Tab>
  preExistingKeys: Set<string>
  /** Mutated by hydrate* / merge* — keys added during this restore pass. */
  addedTabKeys: Set<string>
}

/**
 * Hydrate AGENT tabs from the worker's ListAgents response. Bare tabs
 * already inserted by the live reconciler are filled with their
 * worker-side metadata; otherwise a new tab is added on the resolved
 * tile.
 */
function hydrateAgents(
  filteredAgents: Array<{ id: string, workerId: string }> & Awaited<ReturnType<typeof fanOutTabsToWorkers>>['agents'],
  ctx: RestoreCtx,
): void {
  for (const a of filteredAgents) {
    const key = tabKey({ type: TabType.AGENT, id: a.id })
    const agentFields = protoToAgentTabFields(a.workerId, a)
    if (ctx.preExistingKeys.has(key)) {
      // Hydrate the reconciler-added bare tab with worker-side
      // metadata. Keep the reconciler's CRDT-driven fields intact —
      // the projection is authoritative for tile_id / position /
      // worker_id.
      ctx.tabStore.updateTab(TabType.AGENT, a.id, agentFields)
      ctx.addedTabKeys.add(key)
      continue
    }
    let tileId = ctx.tabTileMap.get(key) ?? ctx.defaultTileId
    if (!ctx.validTileIds.has(tileId as string))
      tileId = ctx.defaultTileId
    ctx.tabStore.addTab({
      type: TabType.AGENT,
      id: a.id,
      tileId,
      ...agentFields,
    }, { activate: false })
    ctx.addedTabKeys.add(key)
  }
}

/**
 * Hydrate TERMINAL tabs from each worker's ListTerminals response.
 * Mirrors hydrateAgents but preserves any display fields (screen,
 * git status, …) the cached snapshot carries forward.
 */
function hydrateTerminals(
  terminalResults: Awaited<ReturnType<typeof fanOutTabsToWorkers>>['terminalsByWorker'],
  ctx: RestoreCtx,
): void {
  for (const { workerId, terminals: terms } of terminalResults) {
    if (terms === null)
      continue
    for (const term of terms) {
      const key = tabKey({ type: TabType.TERMINAL, id: term.terminalId })
      const fresh = protoToTerminalTab(workerId, term)
      const previous = ctx.cachedByKey.get(key)
      const fields = preserveTerminalDisplayFields(preserveNonEmptyGitFields(fresh, previous), previous)
      if (ctx.preExistingKeys.has(key)) {
        // Same hydration story as agents: the reconciler adds bare
        // TERMINAL tabs; we own the title / shell screen / git fields
        // the worker just returned.
        ctx.tabStore.updateTab(TabType.TERMINAL, term.terminalId, { ...fresh, ...fields })
        ctx.addedTabKeys.add(key)
        continue
      }
      let tileId = ctx.tabTileMap.get(key) ?? ctx.defaultTileId
      if (!ctx.validTileIds.has(tileId as string))
        tileId = ctx.defaultTileId
      ctx.tabStore.addTab({ ...fresh, ...fields, tileId }, { activate: false })
      ctx.addedTabKeys.add(key)
    }
  }
}

/**
 * Add hub-listed tabs that no worker returned (e.g. worker offline or
 * agent inactive after restart) so they stay visible in the UI.
 * Uses the cached snapshot to preserve display fields when available.
 */
function mergeHubOnlyTabs(
  tabsResp: { tabs: Array<{ tabType: TabType, tabId: string, workerId: string }> } | null | undefined,
  ctx: RestoreCtx,
): void {
  if (!tabsResp?.tabs)
    return
  for (const t of tabsResp.tabs) {
    const key = tabKey({ type: t.tabType, id: t.tabId })
    if (ctx.addedTabKeys.has(key) || ctx.preExistingKeys.has(key))
      continue
    const cachedTab = ctx.cachedByKey.get(key)
    let tileId = ctx.tabTileMap.get(key) ?? ctx.defaultTileId
    if (!ctx.validTileIds.has(tileId as string))
      tileId = ctx.defaultTileId
    // Preserve any cached tab fields (screen/cols/git info, etc.) and
    // overlay the hub's authoritative identity + worker. Branch on the
    // wire enum so the resulting object's `type` is a literal matching
    // one variant of the Tab union.
    const base = {
      id: t.tabId,
      tileId,
      workerId: t.workerId,
    }
    if (t.tabType === TabType.AGENT)
      ctx.tabStore.addTab({ ...cachedTab, ...base, type: TabType.AGENT }, { activate: false })
    else if (t.tabType === TabType.TERMINAL)
      ctx.tabStore.addTab({ ...cachedTab, ...base, type: TabType.TERMINAL }, { activate: false })
    else if (t.tabType === TabType.FILE)
      ctx.tabStore.addTab({ ...cachedTab, ...base, type: TabType.FILE }, { activate: false })
    ctx.addedTabKeys.add(key)
  }
}

/**
 * Merge any locally-moved tabs from the registry snapshot that the
 * server hasn't surfaced yet (cross-workspace moves can still be in
 * flight when ListTabs returned).
 */
function mergeCachedMovedTabs(
  cached: { tabs: Tab[] } | undefined | null,
  ctx: RestoreCtx,
): void {
  if (!cached || cached.tabs.length === 0)
    return
  const existingKeys = new Set(ctx.tabStore.state.tabs.map(t => tabKey(t)))
  for (const snapTab of cached.tabs) {
    if (!existingKeys.has(tabKey(snapTab))) {
      ctx.tabStore.addTab({ ...snapTab, tileId: ctx.defaultTileId }, { activate: false })
    }
  }
}

/**
 * Read per-tile / per-workspace active-tab pointers from sessionStorage
 * and re-apply them. Also picks the workspace's overall active tab,
 * falling back to the first live tab when sessionStorage is empty or
 * corrupt.
 */
function restoreActiveAndFocusedTab(
  activeId: string,
  ctx: RestoreCtx,
  layoutStore: ReturnType<typeof createLayoutStore>,
): void {
  // Build a key→tab map once so the inner tile-active lookup is O(1)
  // instead of O(tabs) per tile.
  const liveByKey = new Map<string, Tab>()
  for (const t of ctx.tabStore.state.tabs)
    liveByKey.set(tabKey(t), t)
  try {
    const tileActiveJson = sessionStorageGet<string>(tileActiveTabsKey(activeId))
    if (tileActiveJson) {
      const tileActiveTabs = JSON.parse(tileActiveJson) as Record<string, string>
      for (const [tileId, key] of Object.entries(tileActiveTabs)) {
        const parsed = parseTabKey(key)
        const live = liveByKey.get(key)
        if (parsed && live && live.tileId === tileId) {
          ctx.tabStore.setActiveTabForTile(tileId, parsed.type, parsed.id)
        }
      }
    }
  }
  catch {
    // Ignore corrupt sessionStorage data
  }

  // Ensure every tile with tabs has an active tab
  ctx.tabStore.initMissingTileActiveTabs()

  const savedKey = sessionStorageGet<string>(activeTabKey(activeId))
  const parsedSaved = savedKey ? parseTabKey(savedKey) : null
  if (savedKey && parsedSaved && liveByKey.has(savedKey)) {
    const { type: tabType, id: tabId } = parsedSaved
    ctx.tabStore.setActiveTab(tabType, tabId)
    const restoredTab = ctx.tabStore.getTabByKey(savedKey)
    if (restoredTab?.tileId) {
      ctx.tabStore.setActiveTabForTile(restoredTab.tileId, tabType, tabId)
    }
  }
  else if (ctx.tabStore.state.tabs.length > 0) {
    const firstTab = ctx.tabStore.state.tabs[0]
    ctx.tabStore.activateTab(firstTab.tileId ?? '', firstTab.type, firstTab.id)
  }

  // Restore focused tile from sessionStorage
  const savedFocusedTile = sessionStorageGet<string>(focusedTileKey(activeId))
  if (savedFocusedTile && ctx.validTileIds.has(savedFocusedTile)) {
    layoutStore.setFocusedTile(savedFocusedTile)
  }
}

interface UseWorkspaceRestoreOpts {
  getActiveWorkspaceId: () => string | null | undefined
  getOrgId: () => string | undefined
  tabStore: ReturnType<typeof createTabStore>
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore?: FloatingWindowStoreType
  chatStore: ReturnType<typeof createChatStore>
  controlStore: ReturnType<typeof createControlStore>
  agentSessionStore: ReturnType<typeof createAgentSessionStore>
  registry: WorkspaceStoreRegistryType
  setWorkspaceLoading: (v: boolean) => void
  /**
   * Kicks off tab loading for a non-active workspace. Invoked alongside the
   * active workspace's ListTabs on fresh loads so sibling workspaces that
   * were expanded in the sidebar get their tabs fetched in the same batch.
   */
  onExpandWorkspace?: (workspaceId: string) => void
}

export function useWorkspaceRestore(opts: UseWorkspaceRestoreOpts) {
  const {
    getActiveWorkspaceId,
    getOrgId,
    tabStore,
    layoutStore,
    registry,
    setWorkspaceLoading,
  } = opts

  let loadGeneration = 0
  const [terminalHydrationTick, setTerminalHydrationTick] = createSignal(0)
  const terminalHydrationInflight = createInflightCache<string, void>()
  // Per-worker retry: the hydration effect won't re-fire on its own
  // until either the candidate map changes or the tick signal is
  // bumped. The retry's `fire` callback bumps the tick so the effect
  // walks the same candidates again — that's what keeps a transiently-
  // unreachable worker eventually getting hydrated.
  const terminalHydrationRetry = createExponentialBackoff<string>({ initialMs: 500, maxMs: 10_000 })

  const clearTerminalHydrationRetry = (workerId: string) => terminalHydrationRetry.reset(workerId)

  const scheduleTerminalHydrationRetry = (workerId: string) => {
    terminalHydrationRetry.schedule(workerId, () => setTerminalHydrationTick(v => v + 1))
  }

  const hydrateTerminalRecord = (workerId: string, term: Awaited<ReturnType<typeof workerRpc.listTerminals>>['terminals'][number]) => {
    // Hydration only refreshes worker-provided fields; layout fields
    // (tileId, position) on the existing tab are preserved.
    const fields = protoToTerminalTabFields(workerId, term)
    // A transient BatchGetGitStatus failure on the worker surfaces as empty
    // gitBranch/gitOriginUrl. Keep the tab's previous values in that case so
    // the sidebar grouping doesn't flicker out until the next reload.
    const previous = tabStore.getTerminalTab(term.terminalId)
    tabStore.updateTab(
      TabType.TERMINAL,
      term.terminalId,
      preserveTerminalDisplayFields(preserveNonEmptyGitFields(fields, previous), previous),
    )
  }

  onCleanup(() => terminalHydrationRetry.cancelAll())

  createEffect(on([getActiveWorkspaceId, getOrgId], ([activeId, currentOrgId]) => {
    if (!activeId || !currentOrgId)
      return

    const gen = ++loadGeneration

    // Snapshot-save for the outgoing workspace happens inside the
    // URL → activeWorkspaceId sync effect in AppShell.tsx — earlier
    // in the synchronous dispatch than this effect would otherwise
    // run, so the snapshot captures the outgoing workspace's tabs
    // before this effect's non-cached path can `tabStore.clear()`
    // them.

    // Check if we have a cached snapshot for this workspace.
    const cached = registry.get(activeId)
    if (cached?.restored) {
      tabStore.restore(cached)
      // Layout state is bridge-driven (a memo over the CRDT projection
      // for the active workspaceId), so we don't restore a legacy
      // layout snapshot here. The stale snapshot captured during the
      // switch-away effect would set focusedTileId to a tile id that
      // doesn't exist in the projection-derived tree; the layoutStore's
      // focus-invariant effect already snaps focusedTileId to the first
      // leaf when the current focus disappears from the tree.
      //
      // Floating-window state is also bridge-driven: the
      // `floatingWindowStore`'s memo re-derives from the CRDT
      // projection when the activeWorkspaceId flips, so no explicit
      // restore is needed here.
      // Cached AGENT tabs already carry every per-agent field on the Tab
      // record (see protoToAgentTabFields), so tabStore.restore above is
      // sufficient. No separate agent-record cache to restore.

      // Ensure every tile with tabs has an active tab (in case snapshot was
      // taken before per-tile active tabs were properly tracked).
      tabStore.initMissingTileActiveTabs()

      // Activate the tab the user clicked in the sidebar (if any).
      const savedKey = sessionStorageGet<string>(activeTabKey(activeId))
      const parsedSaved = savedKey ? parseTabKey(savedKey) : null
      if (savedKey && parsedSaved && tabStore.state.tabs.some(t => tabKey(t) === savedKey)) {
        const { type: tabType, id: tabId } = parsedSaved
        tabStore.setActiveTab(tabType, tabId)
        const restoredTab = tabStore.getTabByKey(savedKey)
        if (restoredTab?.tileId) {
          tabStore.setActiveTabForTile(restoredTab.tileId, tabType, tabId)
        }
      }

      setWorkspaceLoading(false)
      return
    }

    setWorkspaceLoading(true)
    tabStore.clear()

    // Fetch tabs and layout from hub (single call, no worker needed).
    const tabsLoaded = listTabsForWorkspace(currentOrgId, activeId)
      .catch(() => null)

    // Kick off lazy loads for sibling workspaces whose sidebar rows the user
    // had expanded. Must fire in the same tick as listTabsForWorkspace(activeId)
    // above so the microtask-scoped batcher coalesces them into one RPC; the
    // sibling fire from WorkspaceSectionContent's own mount-time effect runs
    // too late to join the batch.
    if (opts.onExpandWorkspace) {
      for (const siblingId of readExpandedWorkspaceIds()) {
        if (siblingId !== activeId)
          opts.onExpandWorkspace(siblingId)
      }
    }

    // The layout store reads the per-workspace projection of
    // `OrgCrdtState` via a createMemo over `bridge.speculativeState()`.
    // We can't flip `workspaceLoading=false` until that bootstrap
    // arrives, otherwise the layout-store's `FALLBACK_LEAF` paints a
    // synthetic tile whose action handlers are no-ops (the store's
    // mutators bail when the bridge isn't wired). E2E specs that click
    // the split-tile button would then time out waiting for a tile
    // count change. Awaiting the bootstrap also gives a real user a
    // coherent loading state instead of a flickering single-tile
    // placeholder.
    const bootstrapAwaited = awaitWorkspaceBootstrap(
      activeId,
      () => gen === loadGeneration,
    ).catch(() => {
      // Generation-rollover rejection is expected when the user
      // switches workspaces mid-bootstrap; swallow it so the race
      // can settle without surfacing a noise toast.
    })

    const loadTimeout = new Promise<never>((_, reject) =>
      setTimeout(() => reject(new Error('Workspace load timed out after 30s')), 30_000),
    )

    Promise.race([
      Promise.all([tabsLoaded, bootstrapAwaited]),
      loadTimeout,
    ]).then(async ([tabsResp]) => {
      if (gen !== loadGeneration)
        return

      const { agents: fetchedAgents, terminalsByWorker: terminalResults, tabTileMap }
        = await fanOutTabsToWorkers(tabsResp?.tabs ?? [])

      if (gen !== loadGeneration)
        return

      // Merge locally-moved agents from the registry snapshot that the
      // worker hasn't returned yet (cross-workspace moves may still be
      // in flight on the worker side). The cached form is `Tab`, fully
      // populated by `protoToAgentTabFields` on its original
      // hydration; we surface back as AgentInfo-equivalent rows for
      // the loop below.
      const filteredAgents = [...fetchedAgents]
      if (cached && cached.tabs.length > 0) {
        const fetchedIds = new Set(filteredAgents.map(a => a.id))
        for (const snapTab of cached.tabs) {
          if (snapTab.type !== TabType.AGENT || fetchedIds.has(snapTab.id))
            continue
          // Reconstruct a minimal AgentInfo-shaped row from the cached
          // tab. The hydration loop only reads the fields
          // protoToAgentTabFields used in the first place;
          // `agentTabToInfo` populates extra fields
          // (`workspaceId`/`workerName`/`closedAt`/`homeDir`) as empty
          // strings, which the downstream consumers ignore.
          const info = agentTabToInfo(snapTab)
          if (info)
            filteredAgents.push(info as typeof fetchedAgents[number])
        }
      }

      // Batch the entire synchronous restore body. `tabStore.addTab`
      // calls inside the agent / terminal / fallback loops emit CRDT
      // ops via `emitAddTabOp`, which bumps the `pendingVersion` signal
      // the AppShell projection reconciler subscribes to. Without batch
      // the reconciler fires synchronously after each addTab — between
      // loop iterations — and re-adds bare tabs for every agent the
      // loop hasn't reached yet. The next iteration's `addTab` for that
      // agent then hits the dedupe guard and is silently dropped,
      // leaving the tab with no title or `agentProvider`. Batching
      // defers the reactive flush until every tab is in the store with
      // its full metadata, so the reconciler's next pass sees no
      // missing tabs and stays quiet.
      batch(() => {
        // Tab clearing happens once, at the top of the non-cached
        // branch (outgoing-workspace cleanup). Here we keep the store
        // intact so the CRDT-projection reconciler (`AppShell`'s effect
        // on `pendingVersion`) and the file-tab path hydrator
        // (`useTabHydrators`) can pre-populate tabs during this
        // restore's awaits. Clearing inside this branch would wipe
        // those entries, after which `mergeHubOnlyTabs` would re-add
        // file tabs with no `filePath` and the hydrator's
        // `fileTabPaths.pathFor` guard would suppress re-fetching. The
        // hydration phases below use `preExistingKeys` to merge worker
        // metadata onto reconciler-added tabs without removing them.

        // Layout / floating-windows hydration is fully driven by the
        // CRDT projection now. The store's `state.root` is a memo over
        // `bridge.speculativeState()`; it auto-updates when the
        // WatchOrg bootstrap lands. There's no imperative "initial
        // tile" path to seed here — the workspace's seed root was
        // created by the hub's lifecycle outbox during
        // `CreateWorkspace`, and `awaitWorkspaceBootstrap` above held
        // this resolver until the projection saw the root land.

        // Collect tile IDs from both main layout and floating windows.
        const allFloatingTileIds = opts.floatingWindowStore?.getAllTileIds() ?? []
        // `preExistingKeys`: tabs already inserted by the live CRDT-
        // projection reconciler (`AppShell`'s effect on
        // `pendingVersion`), which fires as soon as the `/ws/orgevents`
        // bootstrap lands and beats this slow path on a fresh page
        // load. The reconciler only knows CRDT-driven fields
        // (`tile_id`, `position`, `worker_id`); the hydration phases
        // below fill in titles / agent metadata / terminal screens
        // without removing the reconciler's pointers.
        const ctx: RestoreCtx = {
          tabStore,
          defaultTileId: layoutStore.focusedTileId() ?? undefined,
          validTileIds: new Set([...layoutStore.getAllTileIds(), ...allFloatingTileIds]),
          tabTileMap,
          // O(1) lookup map of cached tabs by key — used by every
          // hydration phase below.
          cachedByKey: cached ? tabsByKey(cached.tabs) : new Map<string, Tab>(),
          preExistingKeys: new Set(tabStore.state.tabs.map(t => tabKey(t))),
          addedTabKeys: new Set<string>(),
        }

        hydrateAgents(filteredAgents, ctx)
        hydrateTerminals(terminalResults, ctx)
        mergeHubOnlyTabs(tabsResp, ctx)
        mergeCachedMovedTabs(cached, ctx)

        if (tabsResp && tabsResp.tabs.length > 0) {
          const posMap = new Map<string, string>()
          for (const t of tabsResp.tabs) {
            posMap.set(tabKey({ type: t.tabType, id: t.tabId }), t.position)
          }
          tabStore.sortByPositions(posMap)
        }

        // FILE tab restore: tabs themselves live in `OrgCrdtState.tabs`
        // (presentation registers only) and their paths live on the
        // worker's `worker_file_tabs` table behind E2EE. On workspace
        // load, the CRDT projection delivers each FILE TabRecord and the
        // `WatchWorkspacePrivateEvents` bootstrap reply populates
        // `fileTabPaths` with each path. The reconciler effect in
        // AppShell.tsx adds local Tab entries for tabs that exist in the
        // CRDT but not yet locally — no sessionStorage round-trip
        // required.

        restoreActiveAndFocusedTab(activeId, ctx, layoutStore)

        // Cache the restored state in the registry. AGENT tabs now
        // carry their full metadata in the tab snapshot, so no separate
        // `agents` slot is required. Floating-window state isn't
        // snapshotted here either — its store is projection-driven and
        // re-derives from the CRDT bridge on workspace re-activation.
        registry.set(activeId, {
          ...tabStore.snapshot(),
          workspaceId: activeId,
          layout: layoutStore.snapshot(),
          restored: true,
          tabsLoaded: true,
        })

        setWorkspaceLoading(false)
      })
    }).catch((err) => {
      log.warn('Workspace restore failed, unblocking UI:', err)
      setWorkspaceLoading(false)
    })
  }))

  // tabsNeedingHydration is the per-tick "what's missing?" view over
  // the tab store. A tab needs hydration when its worker-side data is
  // missing: status undefined, marked DISCONNECTED after a worker
  // outage, or a status event arrived without the accompanying
  // ListTerminals payload. `cols` is the discriminator (not `title`)
  // because shells that don't emit OSC titles would otherwise loop
  // forever.
  //
  // Memoized with a custom-equals comparator so the downstream effect
  // wakes only when the set of (workerId, tabIds) actually changes.
  // Without this, every tab-store mutation (focus toggles, position
  // updates, title changes) re-walked the entire tab list. Now the
  // effect runs only when the membership of "missing" tabs changes
  // or a retry tick fires.
  const tabsNeedingHydration = createMemo<Map<string, string[]>>(
    () => {
      const missingByWorker = new Map<string, string[]>()
      for (const tab of tabStore.state.tabs) {
        if (tab.type !== TabType.TERMINAL || !tab.workerId)
          continue
        const hasWorkerSideData = tab.cols !== undefined
        if (tab.status !== undefined && tab.status !== TerminalStatus.DISCONNECTED && hasWorkerSideData)
          continue
        const ids = missingByWorker.get(tab.workerId) ?? []
        ids.push(tab.id)
        missingByWorker.set(tab.workerId, ids)
      }
      return missingByWorker
    },
    new Map(),
    { equals: missingMapsEqual },
  )

  // Workers seen on the previous effect run. When a worker disappears
  // from the missing-tabs map (workspace switched, worker disconnected,
  // its tabs finished hydrating), we reset its retry slot so a future
  // failure on the same worker id restarts the backoff at `initialMs`
  // instead of resuming mid-sequence. `cancelAll()` on unmount handles
  // the per-mount cleanup; this guards the per-run case.
  let prevHydrationWorkers: Set<string> = new Set()

  createEffect(
    on([terminalHydrationTick, tabsNeedingHydration], ([_, missingByWorker]) => {
      const activeId = getActiveWorkspaceId()
      if (!activeId)
        return

      const currentWorkers = new Set(missingByWorker.keys())
      for (const w of prevHydrationWorkers) {
        if (!currentWorkers.has(w))
          terminalHydrationRetry.reset(w)
      }
      prevHydrationWorkers = currentWorkers

      for (const [workerId, tabIds] of missingByWorker.entries()) {
        if (terminalHydrationInflight.has(workerId))
          continue

        const targetWorkspaceId = activeId
        void terminalHydrationInflight.run(workerId, async () => {
          try {
            const resp = await workerRpc.listTerminals(workerId, { tabIds })
            if (getActiveWorkspaceId() !== targetWorkspaceId)
              return
            const resolvedIDs = new Set(resp.terminals.map(term => term.terminalId))
            batch(() => {
              for (const term of resp.terminals) {
                hydrateTerminalRecord(workerId, term)
              }
            })
            if (tabIds.some(id => !resolvedIDs.has(id))) {
              scheduleTerminalHydrationRetry(workerId)
            }
            else {
              clearTerminalHydrationRetry(workerId)
            }
          }
          catch (err) {
            log.warn('failed to hydrate terminal metadata after restore', { workerId, tabIds, err })
            scheduleTerminalHydrationRetry(workerId)
          }
        })
      }
    }),
  )
}

/**
 * Structural equality for the worker → missing-tab-ids map. Backs the
 * `tabsNeedingHydration` memo so a re-walk over the tab list that
 * produces an identical "missing" set doesn't notify the hydration
 * effect.
 */
function missingMapsEqual(
  a: Map<string, string[]>,
  b: Map<string, string[]>,
): boolean {
  if (a.size !== b.size)
    return false
  for (const [worker, aIds] of a) {
    const bIds = b.get(worker)
    if (!bIds || bIds.length !== aIds.length)
      return false
    // Tab ids inside the slot are appended in iteration order; the
    // tab store's order is itself a derived projection so re-runs
    // produce the same ordering for the same membership.
    for (let i = 0; i < aIds.length; i++) {
      if (aIds[i] !== bIds[i])
        return false
    }
  }
  return true
}
