import type { AddTabOptions, AgentTab, FileDiffBase, FileTab, FileViewMode, RemoveTabOptions, RestorableTabState, Tab, TabStoreState, TerminalTab } from './tab.types'
import type { OrgOp } from '~/generated/leapmux/v1/org_ops_pb'
import type { OpBuilderCtx } from '~/lib/crdt'
import { createMemo } from 'solid-js'
import { createStore, produce } from 'solid-js/store'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import {
  ctxFromBridge,
  getCRDTBridge,
  newBatch,
  setTabPosition as opSetTabPosition,
  tombstoneTab as opTombstoneTab,
  setTabTileId,
  setTabWorkerId,
} from '~/lib/crdt'
import { after, first, positionAtInsertIdx } from '~/lib/lexorank'
import { createLogger } from '~/lib/logger'
import { parseTabKey, tabKey } from './tab.helpers'

const log = createLogger('tab-store')

// CRDT op emission. `emitOps` is the single point of contact for the
// store's mutators: it resolves the bridge context, runs the caller's
// op-builder with that context, and enqueues the resulting batch. Each
// call is a no-op in the test harness (the bridge is unset there) and
// active in AppShell once `setCRDTBridge` runs. The store mutators
// call this in addition to their local optimistic setState — the hub
// echoes the ops back via `/ws/orgevents`, the local pendingOps
// absorbs them, and the reconciliation effect (see
// `reconcileFromProjection`) converges any drift against the canonical
// projection.
//
// Returns the batch id when the bridge was wired (so callers like
// `moveTabToWorkspace` can correlate the emit with a later
// BatchResult), or null when the bridge was unavailable or the
// builder returned an empty op array.

function emitOps(build: (ctx: OpBuilderCtx) => OrgOp[]): string | null {
  const bridge = getCRDTBridge()
  if (!bridge)
    return null
  const ctx = ctxFromBridge(bridge)
  if (!ctx)
    return null
  const ops = build(ctx)
  if (ops.length === 0)
    return null
  return bridge.enqueue(newBatch(ops))
}

export function createTabStore() {
  const [state, setState] = createStore<TabStoreState>({
    tabs: [],
    activeTabKey: null,
    tileActiveTabKeys: {},
  })

  // Monotonic activation counter. Each activation (addTab(activate),
  // setActiveTab, setActiveTabForTile) bumps it and stamps the tab's
  // local-only `mru` field. Global + per-tile MRU is derived from
  // `state.tabs` by sorting on this counter, so no parallel registers
  // need to be maintained in lockstep.
  let mruCounter = 0
  const nextMru = (): number => ++mruCounter

  // Per-tile tab index. The render path filters tabs by tileId 3-4× per
  // Tile per render (tileAgentTabs / tileFileTabs / tileTerminals + the
  // TabBar list). Materializing the index in one pass turns those filters
  // into O(1) Map lookups. The memo tracks `state.tabs` and each tab's
  // `tileId` / `position` reactively, so any membership-changing or
  // ordering mutation invalidates it — no need to bookkeep refresh
  // calls per mutation path.
  //
  // Lists are sorted by LexoRank position (tab_id as a stable tiebreak).
  // Without this sort the visible tab order tracks `state.tabs`
  // insertion order, which can drift from the CRDT-canonical position
  // order: `reconcileFromProjection` updates the `position` field on
  // each tab record but doesn't reorder the array, so a `tile split`
  // / `tile make-grid` / inverse-split from another client lands new
  // positions on the local tabs without their visible order updating
  // until `sortByPositions` runs on workspace restore (i.e. refresh).
  // Sorting inside the memo turns "live, ordered tab list" into the
  // default rather than a workspace-restore artifact.
  const tabsByTile = createMemo(() => {
    const map = new Map<string, Tab[]>()
    for (const tab of state.tabs) {
      if (!tab.tileId)
        continue
      const list = map.get(tab.tileId)
      if (list)
        list.push(tab)
      else
        map.set(tab.tileId, [tab])
    }
    for (const list of map.values()) {
      list.sort((a, b) => {
        const pa = a.position ?? ''
        const pb = b.position ?? ''
        if (pa !== pb)
          return pa < pb ? -1 : 1
        return a.id < b.id ? -1 : a.id > b.id ? 1 : 0
      })
    }
    return map
  })

  // Tab-by-key index. activeTab() and getActiveTabForTile() are called per
  // Tile per render; without this they degrade to O(N tabs) linear find.
  const tabsByKey = createMemo(() => {
    const map = new Map<string, Tab>()
    for (const tab of state.tabs)
      map.set(tabKey(tab), tab)
    return map
  })

  // MRU order: state.tabs sorted by mru descending. Tabs that have
  // never been activated (mru = undefined / 0) fall to the end in
  // insertion order (Array.prototype.sort is stable since ES2019).
  // Two callers need this — close-the-active-tab fallback and the
  // editor's "last agent" lookup — and both want the head element
  // (or a filtered search starting from the head), so we expose it
  // as a memo rather than rebuild on each read.
  const mruOrderMemo = createMemo(() => {
    const ranked = state.tabs.slice().sort((a, b) => (b.mru ?? 0) - (a.mru ?? 0))
    return ranked.map(tabKey)
  })

  function tileMruOrder(tileId: string): string[] {
    const tabs = tabsByTile().get(tileId) ?? []
    return tabs.slice().sort((a, b) => (b.mru ?? 0) - (a.mru ?? 0)).map(tabKey)
  }

  // Inverted index from `tabKey` to the set of tile ids on which that
  // key is currently the active tab. Drives O(1) "is this tab visible
  // anywhere?" lookups (the chat-trimming hot path checks this for
  // every incoming message). `state.tileActiveTabKeys` is a Solid
  // store object, so any per-tile assignment invalidates the memo and
  // the index re-derives.
  const tileIdsByActiveKey = createMemo<Map<string, Set<string>>>(() => {
    const map = new Map<string, Set<string>>()
    for (const [tileId, key] of Object.entries(state.tileActiveTabKeys)) {
      if (!key)
        continue
      const existing = map.get(key)
      if (existing)
        existing.add(tileId)
      else
        map.set(key, new Set([tileId]))
    }
    return map
  })

  // Promote the next-highest-MRU tab on `tileId` to active, returning
  // the new active key (or null when the tile has no tabs left).
  // Shared by every path that removes the tile's current active tab —
  // `removeTab` and `moveTabToTile` both need the same "sort the per-
  // tile MRU once, stamp the next head" sequence.
  function promoteNextActiveOnTile(tileId: string): string | null {
    const next = tileMruOrder(tileId)[0] ?? null
    setState('tileActiveTabKeys', tileId, next)
    return next
  }

  // Bump the named tab's mru counter. No-op when the tab isn't in the
  // store — callers that may race with removal (e.g. clicking a tab
  // just before another client tombstoned it) can call this without
  // an explicit existence check.
  function bumpMru(key: string): void {
    setState('tabs', t => tabKey(t) === key, 'mru', nextMru())
  }

  // Implementation backing the public `updateTab` property below. The
  // typed property casts this to an intersection of three call signatures
  // so callers passing `TabType.AGENT` get `Partial<AgentTab>`-typed
  // `fields` (and similarly for TERMINAL / FILE). Object-literal method
  // shorthand can't carry TypeScript overloads, hence the indirection.
  function updateTab(type: TabType, id: string, fields: Partial<AgentTab> | Partial<TerminalTab> | Partial<FileTab>): void {
    const key = tabKey({ type, id })
    setState('tabs', t => tabKey(t) === key, prev => ({ ...prev, ...fields } as Tab))
  }

  return {
    state,

    addTab(tab: Tab, options: AddTabOptions = {}) {
      const activate = options.activate ?? true
      const key = tabKey(tab)
      // Dedupe by key: if the same (type, id) already lives in the
      // store, don't insert a duplicate row. Two paths can race into
      // this method for the same tab — the CRDT-projection reconciler
      // (step 2) and the worker-restore loop in `useWorkspaceRestore`,
      // for example — and an HMR reload can briefly produce a double
      // bootstrap that hits both. Without this guard the user sees
      // two sidebar rows for one tab (one with the worker-supplied
      // title, one bare from the reconciler) and closing either drops
      // both because `removeTab` filters by key. Silently no-op on
      // the duplicate insert; the existing record holds the
      // authoritative non-CRDT fields (title / agentProvider / git).
      if (tabsByKey().has(key))
        return
      const anchorIdx = options.afterKey
        ? state.tabs.findIndex(t => tabKey(t) === options.afterKey)
        : -1

      if (!tab.position) {
        // Insertion index in the resulting array: just after `anchorIdx`
        // when one was provided, else the end. `positionAtInsertIdx` reads
        // the surrounding neighbours' positions and folds them through `mid`.
        const insertIdx = anchorIdx >= 0 ? anchorIdx + 1 : state.tabs.length
        tab = { ...tab, position: positionAtInsertIdx(state.tabs, insertIdx) }
      }
      if (activate)
        tab = { ...tab, mru: nextMru() }
      setState('tabs', prev => anchorIdx >= 0
        ? [...prev.slice(0, anchorIdx + 1), tab, ...prev.slice(anchorIdx + 1)]
        : [...prev, tab])
      if (activate) {
        setState('activeTabKey', key)
        if (tab.tileId)
          setState('tileActiveTabKeys', tab.tileId, key)
      }
      if (!options.silent) {
        emitOps((ctx) => {
          const ops = [
            setTabTileId(ctx, tab.type, tab.id, tab.tileId ?? ''),
            opSetTabPosition(ctx, tab.type, tab.id, tab.position ?? ''),
          ]
          if (tab.workerId)
            ops.push(setTabWorkerId(ctx, tab.type, tab.id, tab.workerId))
          return ops
        })
      }
    },

    removeTab(type: TabType, id: string, options: RemoveTabOptions = {}) {
      const key = tabKey({ type, id })
      const tab = tabsByKey().get(key)
      const tileId = tab?.tileId

      setState('tabs', prev => prev.filter(t => tabKey(t) !== key))

      // Per-tile fallback: if the removed tab was the tile's active,
      // pick the next-highest-mru tab still in that tile.
      if (tileId && state.tileActiveTabKeys[tileId] === key)
        promoteNextActiveOnTile(tileId)

      // Global fallback: if the removed tab was active, pick the
      // next-highest-mru tab anywhere.
      if (state.activeTabKey === key) {
        const next = mruOrderMemo()
        setState('activeTabKey', next[0] ?? null)
      }
      if (!options.silent)
        emitOps(ctx => [opTombstoneTab(ctx, type, id)])
    },

    setActiveTab(type: TabType, id: string) {
      const key = tabKey({ type, id })
      if (!tabsByKey().has(key))
        return
      bumpMru(key)
      if (state.activeTabKey !== key)
        setState('activeTabKey', key)
      // Clear notification on the newly active tab (only if currently set,
      // so subscribers aren't notified for a no-op).
      setState('tabs', t => tabKey(t) === key && !!t.hasNotification, 'hasNotification', false)
    },

    activeTab(): Tab | null {
      const key = state.activeTabKey
      if (!key)
        return null
      return tabsByKey().get(key) ?? null
    },

    updateTabTitle(type: TabType, id: string, title: string) {
      const key = tabKey({ type, id })
      setState('tabs', t => tabKey(t) === key, 'title', title)
    },

    setNotification(type: TabType, id: string, hasNotification: boolean) {
      const key = tabKey({ type, id })
      setState('tabs', t => tabKey(t) === key, 'hasNotification', hasNotification)
    },

    /** Reorder tabs by moving fromKey to toKey's position. Returns the new LexoRank position. */
    reorderTabs(fromKey: string, toKey: string): string | null {
      const fromIdx = state.tabs.findIndex(t => tabKey(t) === fromKey)
      const toIdx = state.tabs.findIndex(t => tabKey(t) === toKey)
      if (fromIdx === -1 || toIdx === -1 || fromIdx === toIdx)
        return null
      // Clone elements to avoid mutating store proxies directly
      const newTabs = state.tabs.map(t => ({ ...t }))
      const [moved] = newTabs.splice(fromIdx, 1)

      // Swap-on-cross semantics: the dragged tab ends up on the far side of
      // the target relative to where it came from. With `moved` already
      // spliced out, inserting at `toIdx` puts it after the target when
      // moving forward (the target shifted left into index toIdx-1) and
      // before the target when moving backward (the target is still at
      // toIdx). The alternative "always insert before target" is symmetric
      // on paper but degenerates to a no-op when dragging onto the
      // immediate right neighbour.
      const insertIdx = toIdx
      const newPosition = positionAtInsertIdx(newTabs, insertIdx)
      moved.position = newPosition

      newTabs.splice(insertIdx, 0, moved)
      setState('tabs', newTabs)
      emitOps(ctx => [opSetTabPosition(ctx, moved.type, moved.id, newPosition)])
      return newPosition
    },

    /** Sort tabs according to a position map (key -> position). Tabs not in the map keep their relative order at the end. */
    sortByPositions(posMap: Map<string, string>) {
      // Clone elements to avoid mutating store proxies directly
      const sorted = state.tabs.map(t => ({ ...t }))
      // Apply positions from map
      for (const tab of sorted) {
        const pos = posMap.get(tabKey(tab))
        if (pos) {
          tab.position = pos
        }
      }
      sorted.sort((a, b) => {
        const posA = posMap.get(tabKey(a)) ?? ''
        const posB = posMap.get(tabKey(b)) ?? ''
        if (posA && posB)
          return posA.localeCompare(posB)
        if (posA)
          return -1
        if (posB)
          return 1
        return 0
      })
      setState('tabs', sorted)
    },

    clear() {
      setState('tabs', [])
      setState('activeTabKey', null)
      setState('tileActiveTabKeys', {})
    },

    /** Snapshot the current state for registry caching. */
    snapshot(): TabStoreState {
      return {
        tabs: state.tabs.map(t => ({ ...t })),
        activeTabKey: state.activeTabKey,
        tileActiveTabKeys: { ...state.tileActiveTabKeys },
      }
    },

    /** Restore from a previously snapshotted state. */
    restore(snap: RestorableTabState) {
      // Use `setState(path, value)` per-key. Solid stores treat a
      // full-slice replacement (`setState({tabs: newArray})`) as a
      // shallow property set that doesn't always invalidate memos
      // iterating the proxy via `for ... of`; the per-key form is
      // reliable.
      setState('tabs', snap.tabs.map(t => ({ ...t })))
      setState('activeTabKey', snap.activeTabKey)
      setState('tileActiveTabKeys', snap.tileActiveTabKeys ? { ...snap.tileActiveTabKeys } : {})
      // Snapshots carry per-tab `mru` values, but mruCounter is a
      // process-wide counter; bump it past the snapshot's max so the
      // next activation lands strictly above every restored tab.
      let maxMru = 0
      for (const t of snap.tabs) {
        if ((t.mru ?? 0) > maxMru)
          maxMru = t.mru ?? 0
      }
      if (maxMru > mruCounter)
        mruCounter = maxMru
    },

    /** Get tabs for a specific tile. O(1) via the `tabsByTile` memo. */
    getTabsForTile(tileId: string): Tab[] {
      return tabsByTile().get(tileId) ?? []
    },

    /**
     * Get the active tab key for a specific tile. Validates that the
     * stored key still points to a tab actually in this tile —
     * `reconcileFromProjection` can move a tab to a different tile
     * without touching `tileActiveTabKeys`, and the stale entry would
     * otherwise leak as the "active" tab for the source tile.
     */
    getActiveTabKeyForTile(tileId: string): string | null {
      const key = state.tileActiveTabKeys[tileId]
      if (!key)
        return null
      const tab = tabsByKey().get(key)
      if (!tab || tab.tileId !== tileId)
        return null
      return key
    },

    /** Get the active tab object for a specific tile. O(1) via the `tabsByKey` memo. */
    getActiveTabForTile(tileId: string): Tab | null {
      const key = state.tileActiveTabKeys[tileId]
      if (!key)
        return null
      const tab = tabsByKey().get(key)
      if (!tab || tab.tileId !== tileId)
        return null
      return tab
    },

    /** Set the active tab for a specific tile. */
    setActiveTabForTile(tileId: string, type: TabType, id: string) {
      const key = tabKey({ type, id })
      if (!tabsByKey().has(key))
        return
      bumpMru(key)
      if (state.tileActiveTabKeys[tileId] !== key)
        setState('tileActiveTabKeys', tileId, key)
      setState('tabs', t => tabKey(t) === key && !!t.hasNotification, 'hasNotification', false)
    },

    /**
     * activateTab combines the global active-tab + (optional) per-tile
     * active-tab + MRU-bump writes that every callsite was doing as
     * two back-to-back setActiveTab/setActiveTabForTile calls. Single
     * bumpMru fires a single per-tab reactive notification instead of
     * two. tileId may be empty for tabs that aren't yet placed
     * (mid-restore, pre-bridge); the per-tile write is then skipped.
     */
    activateTab(tileId: string, type: TabType, id: string) {
      const key = tabKey({ type, id })
      if (!tabsByKey().has(key))
        return
      bumpMru(key)
      if (state.activeTabKey !== key)
        setState('activeTabKey', key)
      if (tileId && state.tileActiveTabKeys[tileId] !== key)
        setState('tileActiveTabKeys', tileId, key)
      setState('tabs', t => tabKey(t) === key && !!t.hasNotification, 'hasNotification', false)
    },

    /** Set the position of a tab by key. */
    setTabPosition(key: string, position: string) {
      const parsed = parseTabKey(key)
      setState('tabs', t => tabKey(t) === key, 'position', position)
      if (parsed)
        emitOps(ctx => [opSetTabPosition(ctx, parsed.type, parsed.id, position)])
    },

    /** Set the display mode (render/source/split) for a file tab. */
    setTabDisplayMode(type: TabType, id: string, displayMode: string) {
      const key = tabKey({ type, id })
      // The path-form `setState(..., 'displayMode', value)` requires
      // every union member to declare the key; FILE-only fields like
      // `displayMode` don't satisfy that, so use the functional form
      // and only assign when the tab is the right variant.
      setState('tabs', t => tabKey(t) === key, prev => ({ ...prev, displayMode } as FileTab))
    },

    /** Set the file view mode for a file tab. */
    setTabFileViewMode(type: TabType, id: string, mode: FileViewMode) {
      const key = tabKey({ type, id })
      setState('tabs', t => tabKey(t) === key, prev => ({ ...prev, fileViewMode: mode } as FileTab))
    },

    /** Set the file diff base for a file tab. */
    setTabFileDiffBase(type: TabType, id: string, base: FileDiffBase) {
      const key = tabKey({ type, id })
      setState('tabs', t => tabKey(t) === key, prev => ({ ...prev, fileDiffBase: base } as FileTab))
    },

    /**
     * Update arbitrary fields on a tab. The `fields` shape is keyed on
     * `type` so callers passing `TabType.AGENT` get an `AgentTab`-shaped
     * `fields` parameter (and similarly for TERMINAL / FILE). Built as
     * an intersection of three call signatures (assigned via cast)
     * because object-literal method shorthand can't carry TypeScript
     * overloads.
     */
    updateTab: updateTab as
      & ((type: TabType.AGENT, id: string, fields: Partial<AgentTab>) => void)
      & ((type: TabType.TERMINAL, id: string, fields: Partial<TerminalTab>) => void)
      & ((type: TabType.FILE, id: string, fields: Partial<FileTab>) => void),

    /**
     * Apply the same `fields` to every tab matching `predicate` in a single
     * store mutation. Use this when an effect would otherwise call
     * `updateTab` in a loop — each call walks the tabs array, so batching is
     * O(N) instead of O(N·K) for K matches.
     */
    updateMatchingTabs(predicate: (tab: Tab) => boolean, fields: Partial<AgentTab> | Partial<TerminalTab> | Partial<FileTab>) {
      setState('tabs', predicate, prev => ({ ...prev, ...fields } as Tab))
    },

    /** Find a terminal tab by its terminal id. */
    getTerminalTab(id: string): TerminalTab | undefined {
      const tab = tabsByKey().get(tabKey({ type: TabType.TERMINAL, id }))
      // tabKey only matches a TERMINAL row, so the union narrows to
      // TerminalTab; cast keeps the public signature accurate.
      return tab as TerminalTab | undefined
    },

    /**
     * Find an agent tab by its agent id. Reactive — consumers that read
     * inside a Solid reactive context re-run when the agent's tab fields
     * change (via `updateTab`) or when the agent is added/removed.
     */
    getAgentTab(id: string): AgentTab | undefined {
      const tab = tabsByKey().get(tabKey({ type: TabType.AGENT, id }))
      // tabKey only matches an AGENT row, so the union narrows to
      // AgentTab; cast keeps the public signature accurate.
      return tab as AgentTab | undefined
    },

    /** Find a tab by its `tabKey(...)` string. O(1) via the `tabsByKey` memo. */
    getTabByKey(key: string): Tab | undefined {
      return tabsByKey().get(key)
    },

    /**
     * True iff the tab is the global active tab or the per-tile
     * active tab on at least one tile. O(1) via the
     * `tileIdsByActiveKey` inverted-index memo — the previous
     * implementation walked `Object.values(tileActiveTabKeys)` on
     * every incoming chat message, which the trimming hot path
     * dominates with non-trivial tile counts.
     */
    isTabActiveAnywhere(type: TabType, id: string): boolean {
      const key = tabKey({ type, id })
      if (state.activeTabKey === key)
        return true
      return tileIdsByActiveKey().has(key)
    },

    /**
     * Find the most-recently-activated tab whose key matches `predicate`.
     * Used by editorRef.store to locate the last-active agent tab after a
     * tab close; the global MRU memo answers in one pass.
     */
    findMruMatching(predicate: (key: string) => boolean): string | undefined {
      return mruOrderMemo().find(predicate)
    },

    /**
     * Per-tile MRU order, derived from `state.tabs`. The list is
     * recomputed on every call (cheap for the per-tile tab counts in
     * practice) and reflects current `tileId` membership — stale tabs
     * never leak across tiles.
     */
    getTileMruOrder(tileId: string): string[] {
      return tileMruOrder(tileId)
    },

    /** Downgrade all running terminal tabs on a worker to disconnected in a single pass. */
    markTerminalsDisconnected(workerId: string) {
      setState(
        'tabs',
        t => t.type === TabType.TERMINAL && t.workerId === workerId && t.status === TerminalStatus.READY,
        prev => ({ ...prev, status: TerminalStatus.DISCONNECTED } as TerminalTab),
      )
    },

    /** Mark a terminal tab as exited. No-op if the tab is missing or already exited. */
    markTerminalExited(id: string) {
      setState(
        'tabs',
        t => t.type === TabType.TERMINAL
          && t.id === id
          && (t.status !== TerminalStatus.EXITED || !t.contentReady || t.startupMessage !== undefined),
        prev => ({
          ...prev,
          status: TerminalStatus.EXITED,
          startupMessage: undefined,
          contentReady: true,
        }),
      )
    },

    /** Idempotently mark a terminal as having painted non-whitespace content. */
    markTerminalContentReady(id: string) {
      setState(
        'tabs',
        t => t.type === TabType.TERMINAL && t.id === id && !t.contentReady,
        prev => ({ ...prev, contentReady: true } as TerminalTab),
      )
    },

    /**
     * Advance a terminal tab's resume cursor. Callers pass the offset
     * returned by `applyTerminalData`, which has already applied its
     * snapshot-vs-incremental return rule. Predicate skips no-op writes
     * so same-value updates don't fire reactive notifications.
     */
    setTerminalLastOffset(id: string, offset: number) {
      setState(
        'tabs',
        t => t.type === TabType.TERMINAL && t.id === id && t.lastOffset !== offset,
        prev => ({ ...prev, lastOffset: offset } as TerminalTab),
      )
    },

    /** For each tile that has tabs but no active tab, activate the first tab. */
    initMissingTileActiveTabs() {
      const tileIds = new Set(state.tabs.map(t => t.tileId).filter(Boolean) as string[])
      for (const tileId of tileIds) {
        if (!this.getActiveTabKeyForTile(tileId)) {
          const firstTab = state.tabs.find(t => t.tileId === tileId)
          if (firstTab) {
            const key = tabKey(firstTab)
            setState('tileActiveTabKeys', tileId, key)
          }
        }
      }
    },

    /** Move a tab to a different tile, cleaning up source tile state. */
    moveTabToTile(key: string, targetTileId: string) {
      const sourceTab = tabsByKey().get(key)
      const sourceTileId = sourceTab?.tileId
      // Drop on debug — the `allTabKeys` snapshot allocates one
      // entry per tab in the workspace on every drag-drop, just for
      // diagnostic output that only matters under debug logging.
      log.debug('moveTabToTile:start', {
        key,
        targetTileId,
        sourceTileId,
        sourceTabExists: sourceTab !== undefined,
        allTabKeys: state.tabs.map(t => ({ key: tabKey(t), tileId: t.tileId })),
      })
      setState('tabs', t => tabKey(t) === key, 'tileId', targetTileId)
      log.debug('moveTabToTile:afterSetState', {
        afterTabsAtSource: sourceTileId ? (tabsByTile().get(sourceTileId)?.length ?? 0) : null,
        afterTabsAtTarget: tabsByTile().get(targetTileId)?.length ?? 0,
        affectedTabTileId: tabsByKey().get(key)?.tileId,
      })

      // Per-tile active fallback for the source: if the moved tab was
      // active there, pick the next-highest-mru tab still in that tile.
      if (sourceTileId && sourceTileId !== targetTileId
        && state.tileActiveTabKeys[sourceTileId] === key) {
        promoteNextActiveOnTile(sourceTileId)
      }
      const parsed = parseTabKey(key)
      if (parsed && sourceTileId !== targetTileId)
        emitOps(ctx => [setTabTileId(ctx, parsed.type, parsed.id, targetTileId)])
    },

    /**
     * Reassign every tab whose tileId is in `oldTileIds` to `newTileId`,
     * merging their per-tile active state into the new tile and deleting
     * the source tiles' state. Used by the "Convert to tile" close-grid mode.
     */
    reassignTabsToTile(oldTileIds: string[], newTileId: string) {
      const oldSet = new Set(oldTileIds)
      // Snapshot the affected tab identities BEFORE the bulk update,
      // so we can emit per-tab SetTabRegister(tile_id) ops in one
      // batch. After the setState mutates `tileId` in place, the same
      // predicate would no longer match.
      const reassigned = state.tabs
        .filter(tab => tab.tileId !== undefined && oldSet.has(tab.tileId) && tab.tileId !== newTileId)
        .map(tab => ({ type: tab.type, id: tab.id, tileId: newTileId }))
      // Bulk-update tab.tileId. The per-tile MRU memo auto-recomputes
      // because each moved tab's tileId now points at newTileId.
      setState('tabs', tab => tab.tileId !== undefined && oldSet.has(tab.tileId), 'tileId', newTileId)

      // Active-tab merge: prefer the reconciler's newTile pick if it
      // exists; else pick the first source tile that had an active;
      // else fall back to the merged-tile MRU head.
      let mergedActive: string | null = this.getActiveTabKeyForTile(newTileId)
      if (!mergedActive) {
        for (const id of oldTileIds) {
          if (id === newTileId)
            continue
          const a = state.tileActiveTabKeys[id]
          if (a) {
            mergedActive = a
            break
          }
        }
      }
      if (!mergedActive) {
        const merged = tileMruOrder(newTileId)
        mergedActive = merged[0] ?? null
      }
      setState('tileActiveTabKeys', newTileId, mergedActive)

      this.cleanupTiles(oldTileIds.filter(id => id !== newTileId))

      // Emit one batch of SetTabRegister(tile_id) ops so the CRDT
      // reflects the new ownership. All reassigned tabs share the
      // same newTileId, so a single batch keeps the wire bounded
      // and preserves atomicity at the hub.
      if (reassigned.length > 0)
        emitOps(ctx => reassigned.map(it => setTabTileId(ctx, it.type, it.id, it.tileId)))
    },

    /**
     * Move every tab on `sourceTileId` onto `targetTileId`, appending to the
     * end of the target's tab list, and clean up the source tile's active
     * state. Differs from `reassignTabsToTile` (used for grid → tile merge):
     * the target here is an existing tile that already has its own active tab,
     * which is preserved.
     */
    mergeTabsIntoTile(sourceTileId: string, targetTileId: string) {
      if (sourceTileId === targetTileId)
        return
      const byTile = tabsByTile()
      const sourceTabs = byTile.get(sourceTileId) ?? []
      const moveOps: { type: TabType, id: string, tileId: string, position: string }[] = []
      if (sourceTabs.length > 0) {
        const targetTabs = byTile.get(targetTileId) ?? []
        let lastPos = targetTabs.at(-1)?.position ?? ''
        const sourceKeys = new Set(sourceTabs.map(tabKey))
        setState('tabs', produce((tabs) => {
          for (const tab of tabs) {
            if (sourceKeys.has(tabKey(tab))) {
              tab.tileId = targetTileId
              const newPos = lastPos ? after(lastPos) : first()
              tab.position = newPos
              lastPos = newPos
              moveOps.push({ type: tab.type, id: tab.id, tileId: targetTileId, position: newPos })
            }
          }
        }))

        // Adopt source's active only when the target had none.
        if (!this.getActiveTabKeyForTile(targetTileId)) {
          const sourceActive = state.tileActiveTabKeys[sourceTileId] ?? null
          if (sourceActive)
            setState('tileActiveTabKeys', targetTileId, sourceActive)
        }

        // Emit one batch carrying tile_id + position for each moved
        // tab. The two ops per tab are co-scheduled by the
        // OpsSubmitter's 16ms aggregator and validated together at
        // the hub.
        if (moveOps.length > 0) {
          emitOps((ctx) => {
            const batchOps: OrgOp[] = []
            for (const m of moveOps) {
              batchOps.push(setTabTileId(ctx, m.type, m.id, m.tileId))
              batchOps.push(opSetTabPosition(ctx, m.type, m.id, m.position))
            }
            return batchOps
          })
        }
      }
      this.cleanupTile(sourceTileId)
    },

    /**
     * Drop per-tile active-tab entries for a removed tile. Tile ids
     * are minted from a monotonic counter and never reused, so without this
     * the records leak into every snapshot until workspace switch.
     */
    cleanupTile(tileId: string) {
      this.cleanupTiles([tileId])
    },

    /**
     * Bulk variant of `cleanupTile`: drops active-tab entries for every
     * tile id in `tileIds` using one `produce`. Callers closing a grid
     * or floating window otherwise issue one `setState` call per tile —
     * the batched form fires the map's reactive notification at most once.
     */
    cleanupTiles(tileIds: Iterable<string>) {
      const ids = [...tileIds]
      if (ids.length === 0)
        return
      const activeIds = ids.filter(id => state.tileActiveTabKeys[id] !== undefined)
      if (activeIds.length > 0) {
        setState('tileActiveTabKeys', produce((m) => {
          for (const id of activeIds)
            delete m[id]
        }))
      }
    },

    /**
     * Move a tab to a tile in a different workspace via the plan's
     * prescribed single-batch pattern: `SetTabRegister(tile_id=newTile)`
     * + `SetTabRegister(position=newPos)`. The tab's owning workspace
     * is derived from the new tile's ancestor chain; `worker_id` does
     * not change.
     *
     * Crucially this is NOT a remove-from-source + add-to-target
     * sequence: TombstoneTab is remove-wins, so a subsequent
     * SetTabRegister on the same tab_id would be silently dropped at
     * the hub. The single-LWW-write pattern lets the same tab record
     * cross the workspace boundary atomically.
     *
     * `tileId` must be a leaf in the destination workspace's tree;
     * callers resolve it from the workspace registry / projection.
     */
    moveTabToWorkspace(
      type: TabType,
      id: string,
      tileId: string,
      position: string,
    ): string | null {
      return emitOps(ctx => [
        setTabTileId(ctx, type, id, tileId),
        opSetTabPosition(ctx, type, id, position),
      ])
    },

    /**
     * Drive the local `state.tabs` toward the CRDT projection. The
     * caller (typically a `createEffect` in AppShell that watches
     * `bridge.speculativeState()`) passes:
     *
     *   - `workspaceId`         — the active workspace; only tabs
     *                             resolving to this workspace are
     *                             kept locally;
     *   - `renderedTabs`        — projected tabs for the active
     *                             workspace (already filtered by
     *                             ancestor-chain resolution at the
     *                             projection layer);
     *   - `crdtKnownTabIds`     — the full set of tab_ids the CRDT
     *                             has any record for (live or
     *                             tombstoned). Local tabs not in
     *                             this set are left alone — they
     *                             belong to client-only flows that
     *                             register with the CRDT lazily
     *                             (e.g. FILE tabs pre-E2EE
     *                             registration).
     *
     * All mutations on this path are silent (`silent: true`) so the
     * reconciler never emits ops in response to ops it just
     * absorbed. Per-tile MRU follows tab.tileId automatically via the
     * derived MRU memo, so cross-tile migrations don't need explicit
     * per-tile bookkeeping here.
     */
    reconcileFromProjection(opts: {
      workspaceId: string
      renderedTabs: { tabType: TabType, tabId: string, tileId: string, position: string, workerId: string }[]
      crdtKnownTabIds: Set<string>
      // Tabs whose CRDT TabRecord exists, is NOT tombstoned, but whose
      // tile chain currently dead-ends at an unknown node id. These
      // arise transiently when the hub broadcasts a tile split / make-
      // grid: the Batch frame ships the SetTabRegister(tile_id=<new
      // cell>) op, but the new cell node arrives in a separate
      // EntityMaterialized frame, so for the window between the two
      // frames the tab's tile_id is a "valid CRDT value pointing at a
      // node we haven't installed yet". Dropping such tabs in step 1
      // is the bug that made `tile make-grid` look like it deleted
      // every tab on the target tile until a manual page refresh.
      transientUnresolvableTabIds?: Set<string>
    }) {
      const projByKey = new Map<string, typeof opts.renderedTabs[number]>()
      for (const t of opts.renderedTabs) {
        projByKey.set(tabKey({ type: t.tabType, id: t.tabId }), t)
      }

      // 1) Drop tabs the CRDT canonicalized as gone-from-this-workspace
      //    or fully tombstoned. Skip tabs the CRDT doesn't know — they
      //    belong to client-only flows. Also skip tabs whose CRDT
      //    record is alive but whose tile chain hasn't materialized
      //    yet: dropping them here is the visible source of the
      //    "tabs vanish after make-grid until refresh" bug.
      const toRemove: Array<{ type: TabType, id: string }> = []
      const transient = opts.transientUnresolvableTabIds
      for (const local of state.tabs) {
        const key = tabKey(local)
        if (projByKey.has(key))
          continue
        if (!opts.crdtKnownTabIds.has(local.id))
          continue
        if (transient && transient.has(local.id))
          continue
        toRemove.push({ type: local.type, id: local.id })
      }
      for (const r of toRemove) {
        this.removeTab(r.type, r.id, { silent: true })
      }

      // 2) Add tabs the projection has but the local store doesn't,
      //    seeded with only CRDT-driven fields. Worker metadata
      //    (title, screen, etc.) is filled in by the private-event
      //    stream after the tab is on screen.
      const localByKey = tabsByKey()
      for (const r of opts.renderedTabs) {
        const key = tabKey({ type: r.tabType, id: r.tabId })
        if (localByKey.get(key))
          continue
        // Branch on the projected wire enum so the resulting object's
        // `type` is a literal matching one variant of the Tab union.
        const base = {
          id: r.tabId,
          tileId: r.tileId,
          position: r.position,
          workerId: r.workerId || undefined,
        }
        if (r.tabType === TabType.AGENT)
          this.addTab({ type: TabType.AGENT, ...base }, { activate: false, silent: true })
        else if (r.tabType === TabType.TERMINAL)
          this.addTab({ type: TabType.TERMINAL, ...base }, { activate: false, silent: true })
        else if (r.tabType === TabType.FILE)
          this.addTab({ type: TabType.FILE, ...base }, { activate: false, silent: true })
      }

      // 3) For tabs that exist on both sides, sync CRDT-driven fields
      //    (tile_id, position, worker_id) when they differ. Use a
      //    single `produce` so the reactive subscribers fire once.
      //    Per-tile MRU/active state follows tile_id automatically via
      //    the derived memo — no per-tile bookkeeping needed.
      const updates: Array<{ key: string, tileId?: string, position?: string, workerId?: string }> = []
      for (const r of opts.renderedTabs) {
        const key = tabKey({ type: r.tabType, id: r.tabId })
        const local = localByKey.get(key)
        if (!local)
          continue
        const u: typeof updates[number] = { key }
        if ((local.tileId ?? '') !== r.tileId)
          u.tileId = r.tileId
        if ((local.position ?? '') !== r.position)
          u.position = r.position
        if (r.workerId && (local.workerId ?? '') !== r.workerId)
          u.workerId = r.workerId
        if (u.tileId !== undefined || u.position !== undefined || u.workerId !== undefined)
          updates.push(u)
      }
      if (updates.length > 0) {
        setState('tabs', produce((tabs) => {
          // Build a key→index map once so the inner loop is O(K) instead
          // of O(N×K). Reconciliation runs per CRDT bridge tick — every
          // pointermove during a cross-workspace drag — so the per-tick
          // walk over `tabs` to find each update's row used to dominate.
          const indexByKey = new Map<string, number>()
          for (let i = 0; i < tabs.length; i++)
            indexByKey.set(tabKey(tabs[i]), i)
          for (const u of updates) {
            const idx = indexByKey.get(u.key)
            if (idx === undefined)
              continue
            if (u.tileId !== undefined)
              tabs[idx].tileId = u.tileId
            if (u.position !== undefined)
              tabs[idx].position = u.position
            if (u.workerId !== undefined)
              tabs[idx].workerId = u.workerId
          }
        }))

        // Newly-arrived-on-tile cleanup: when the reconciler moves a
        // tab onto a tile that has no active tab yet (the typical
        // split / make-grid case), promote it. Per-tile MRU follows
        // tile_id automatically through the memo; this just owns the
        // explicit-active register.
        //
        // Two passes so focus migrates with the active tab even when
        // several tabs land on the same destination tile (the
        // make-grid case where every tab on the source LEAF moves to
        // cell[0,0] in one batch). First pass: if the workspace's
        // currently-active tab is among the movers, anchor it on its
        // new tile. Second pass: fill in any destination tile that
        // still has no active using whichever migrator arrived first.
        // Without the first pass, the previously-active tab loses its
        // focused state because some other migrator wins the
        // `tileActiveTabKeys` slot (which one is order-dependent).
        const activeKey = state.activeTabKey
        for (const u of updates) {
          if (u.tileId === undefined || u.tileId === '')
            continue
          if (activeKey && u.key === activeKey)
            setState('tileActiveTabKeys', u.tileId, u.key)
        }
        for (const u of updates) {
          if (u.tileId === undefined || u.tileId === '')
            continue
          if (!this.getActiveTabKeyForTile(u.tileId))
            setState('tileActiveTabKeys', u.tileId, u.key)
        }
      }
    },

  }
}
