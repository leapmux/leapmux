import type { listTerminals } from '~/api/workerRpc'
import type { AgentInfo, AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { createMemo } from 'solid-js'
import { createStore, produce } from 'solid-js/store'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { after, first, positionAtInsertIdx } from '~/lib/lexorank'

export type FileViewMode = 'working' | 'head' | 'staged' | 'unified-diff' | 'split-diff'
export type FileDiffBase = 'head-vs-working' | 'head-vs-staged'
export type FileOpenSource = 'all' | 'changed' | 'staged' | 'unstaged'

export interface Tab {
  type: TabType
  id: string
  title?: string
  hasNotification?: boolean
  position?: string
  tileId?: string
  workerId?: string
  workingDir?: string
  filePath?: string
  displayMode?: string
  fileViewMode?: FileViewMode
  fileDiffBase?: FileDiffBase
  fileOpenSource?: FileOpenSource
  agentProvider?: AgentProvider
  gitBranch?: string
  gitOriginUrl?: string
  /**
   * Absolute working-tree root of the tab's enclosing git repository
   * (from `git rev-parse --show-toplevel`). Used to group origin-less
   * repos ("local" repos) in the sidebar tree; the same toplevel means
   * the same repo, different toplevels mean different repos.
   */
  gitToplevel?: string
  gitDiffAdded?: number
  gitDiffDeleted?: number
  gitDiffUntracked?: number
  // -------------------------------------------------------------------------
  // Fields below are only meaningful for `TabType.TERMINAL`.
  // -------------------------------------------------------------------------
  status?: TerminalStatus
  /** Working directory the shell was originally spawned in. */
  shellStartDir?: string
  /** Last-known screen snapshot for fast visual restore. */
  screen?: Uint8Array
  /**
   * Cumulative PTY byte offset this tab has already applied to its
   * xterm. Seeded at hydration from the backend's screen_end_offset
   * (the offset at the end of `screen`, which equals `screen.length`
   * before the ring wraps and is larger once bytes fall off), then
   * advanced monotonically as TerminalData events arrive. Echoed back
   * to the backend as WatchTerminalEntry.after_offset on resubscribe
   * so the handler skips bytes we already have.
   */
  lastOffset?: number
  cols?: number
  rows?: number
  /** Error string from TerminalStatusChange when status is STARTUP_FAILED. */
  startupError?: string
  /** Phase label from TerminalStatusChange.startup_message while status is STARTING (e.g. "Starting zsh…"). */
  startupMessage?: string
  /**
   * True once the terminal has emitted any non-whitespace output to the
   * xterm buffer. Drives the "Starting terminal…" overlay — kept visible
   * over the mounted xterm until the shell has actually painted its
   * prompt (not just the moment the PTY was spawned). Preseeded true on
   * reconnect when a screen snapshot is restored.
   */
  contentReady?: boolean
}

type ProtoTerminal = Awaited<ReturnType<typeof listTerminals>>['terminals'][number]

/**
 * Worker-provided fields for a terminal tab, ready to spread into a `Tab`
 * or pass to `updateTab`. Excludes layout-specific fields (`type`, `id`,
 * `tileId`, `position`) which the caller controls.
 */
export function protoToTerminalTabFields(workerId: string, term: ProtoTerminal): Partial<Tab> {
  let status: TerminalStatus
  switch (term.status) {
    case TerminalStatus.STARTING:
      status = TerminalStatus.STARTING
      break
    case TerminalStatus.STARTUP_FAILED:
      status = TerminalStatus.STARTUP_FAILED
      break
    case TerminalStatus.READY:
    default:
      status = term.exited ? TerminalStatus.EXITED : TerminalStatus.READY
  }
  return {
    title: term.title || undefined,
    workerId,
    workingDir: term.workingDir || undefined,
    shellStartDir: term.shellStartDir || undefined,
    screen: term.screen.length > 0 ? term.screen : undefined,
    lastOffset: term.screen.length > 0 ? Number(term.screenEndOffset) : undefined,
    cols: term.cols || undefined,
    rows: term.rows || undefined,
    gitBranch: term.gitBranch || undefined,
    gitOriginUrl: term.gitOriginUrl || undefined,
    gitToplevel: term.gitToplevel || undefined,
    status,
    startupError: term.startupError || undefined,
    startupMessage: term.startupMessage || undefined,
    // Any persisted screen → the shell already painted content; skip the
    // "Starting…" overlay on reconnect to avoid a flash.
    contentReady: term.screen.length > 0 ? true : undefined,
  }
}

/** Build a terminal `Tab` from a `listTerminals` proto record. */
export function protoToTerminalTab(workerId: string, term: ProtoTerminal): Tab {
  return {
    type: TabType.TERMINAL,
    id: term.terminalId,
    ...protoToTerminalTabFields(workerId, term),
  }
}

/** The three tab fields derived from git status. */
export type GitTabFields = Pick<Tab, 'gitBranch' | 'gitOriginUrl' | 'gitToplevel'>

/**
 * Normalize a git-info triple (from AgentGitStatus or a flat
 * TerminalStatusChange) into the tab shape, mapping empty strings to
 * undefined so comparisons stay sane.
 */
export function toGitTabFields(branch: string, originUrl: string, toplevel: string): GitTabFields {
  return {
    gitBranch: branch || undefined,
    gitOriginUrl: originUrl || undefined,
    gitToplevel: toplevel || undefined,
  }
}

/** True when `next` would change any of the three git fields on `tab`. */
export function gitTabFieldsDiffer(tab: GitTabFields, next: GitTabFields): boolean {
  return tab.gitBranch !== next.gitBranch
    || tab.gitOriginUrl !== next.gitOriginUrl
    || tab.gitToplevel !== next.gitToplevel
}

/**
 * Directory whose git status determines a tab's branch/origin. Mirror of
 * `gitutil.ResolveGitDir` on the backend — both sides must resolve the
 * same way so `resolveOptimisticGitInfo`'s dir-match guard stays correct.
 * Agent tabs never carry a shellStartDir so this collapses to workingDir
 * for them.
 */
function effectiveGitDir(tab: Pick<Tab, 'shellStartDir' | 'workingDir'>): string {
  return tab.shellStartDir || tab.workingDir || ''
}

/**
 * Fills in empty gitBranch / gitOriginUrl on a fresh server-provided fields
 * record from the previous tab's values. Guards against a transient
 * git-status failure on the worker (nil gs from BatchGetGitStatus) wiping
 * out authoritative values the tab already had, which would drop the tab
 * out of its sidebar group until the next workspace reload. Per-field so
 * one legitimately-cleared field (e.g. user removed `origin` remote) still
 * updates instead of being masked by the preserved branch.
 *
 * Callers: the hydration/refresh paths that rebuild tabs from ListTerminals
 * / ListAgents responses. The TerminalStatusChange / statusChange handlers
 * already guard on `(branch || origin)` so they intentionally skip empty
 * broadcasts without needing this helper.
 */
export function preserveNonEmptyGitFields(
  fresh: Partial<Tab>,
  previous: Pick<Tab, 'gitBranch' | 'gitOriginUrl' | 'gitToplevel'> | null | undefined,
): Partial<Tab> {
  if (!previous)
    return fresh
  const next: Partial<Tab> = { ...fresh }
  if (!next.gitBranch && previous.gitBranch)
    next.gitBranch = previous.gitBranch
  if (!next.gitOriginUrl && previous.gitOriginUrl)
    next.gitOriginUrl = previous.gitOriginUrl
  if (!next.gitToplevel && previous.gitToplevel)
    next.gitToplevel = previous.gitToplevel
  return next
}

/**
 * Optimistic git branch/origin to seed on a freshly-opened agent or terminal
 * tab. A new tab starts with empty gitBranch/gitOriginUrl and only learns
 * them once the async phase-1 startup broadcasts TerminalStatusChange; in
 * that window the sidebar renders the tab under the workspace instead of
 * nested under its branch (WorkspaceTabTree.buildTree groups solely on
 * gitOriginUrl). Seeding avoids that flash.
 *
 * Only safe to seed when the active tab and the new tab resolve to the same
 * git directory — otherwise the seeded values would be wrong for the new
 * tab's repo. File tabs have no authoritative git info so they never seed.
 */
export function resolveOptimisticGitInfo(
  activeTab: Tab | null | undefined,
  newTab: Pick<Tab, 'shellStartDir' | 'workingDir'>,
): { gitBranch?: string, gitOriginUrl?: string, gitToplevel?: string } {
  if (!activeTab)
    return {}
  if (activeTab.type !== TabType.AGENT && activeTab.type !== TabType.TERMINAL)
    return {}
  // Needs at least an origin or a toplevel — otherwise there is no grouping
  // value to seed, and the sidebar would still fall through to ungrouped
  // until the authoritative broadcast arrives.
  if (!activeTab.gitOriginUrl && !activeTab.gitToplevel)
    return {}
  const activeDir = effectiveGitDir(activeTab)
  const newDir = effectiveGitDir(newTab)
  if (!activeDir || activeDir !== newDir)
    return {}
  return {
    gitBranch: activeTab.gitBranch || undefined,
    gitOriginUrl: activeTab.gitOriginUrl || undefined,
    gitToplevel: activeTab.gitToplevel || undefined,
  }
}

/**
 * Whether the tab's working tree is in a stable state for `git status`.
 *
 * Defers across the entire STARTING window of a worktree-creating agent
 * or terminal. While `git worktree add` is still effectively populating
 * the working tree (or its writes are not yet observable to a separate
 * process running `git status` — seen in practice on at least one
 * filesystem setup), a status query reports every still-unwritten
 * in-index file as deleted, which would otherwise blast bogus diff
 * stats onto the new tab. Waiting for status to leave STARTING is the
 * conservative signal that's known to be reliable: by then phase 2's
 * provider init has completed too, and the worktree has had time to
 * settle.
 *
 * Trade-off: the file tree shows no diff badge for the whole startup
 * (a few seconds), not just phase 0. Acceptable — users don't expect
 * meaningful diff stats while "Starting <provider>…" is on screen.
 *
 * File tabs are always treated as ready — they don't go through the
 * worktree-creating startup pipeline.
 */
export function isTabReadyForGitStatus(
  tab: Tab | null | undefined,
  agent: Pick<AgentInfo, 'status' | 'startupMessage' | 'gitStatus'> | null | undefined,
): boolean {
  if (!tab)
    return true
  if (tab.type === TabType.AGENT) {
    if (!agent)
      return true
    return agent.status !== AgentStatus.STARTING
  }
  if (tab.type === TabType.TERMINAL) {
    return tab.status !== TerminalStatus.STARTING
  }
  return true
}

export interface TabItemOps {
  onClose?: (tab: Tab) => void
  onRename?: (tab: Tab, title: string) => void
  closingKeys?: Set<string>
}

export function tabKey(tab: Tab): string {
  return `${tab.type}:${tab.id}`
}

/**
 * Inverse of `tabKey`. Returns null when the input is malformed (missing
 * colon, non-numeric type) so callers can decide how to handle stale or
 * corrupt persisted keys.
 */
export function parseTabKey(key: string): { type: TabType, id: string } | null {
  const idx = key.indexOf(':')
  if (idx <= 0 || idx === key.length - 1)
    return null
  const typeNum = Number(key.slice(0, idx))
  if (!Number.isInteger(typeNum))
    return null
  return { type: typeNum as TabType, id: key.slice(idx + 1) }
}

export function canCloseTab(readOnly: boolean | undefined, tab: Tab): boolean {
  return !readOnly || tab.type === TabType.FILE
}

export interface TabStoreState {
  tabs: Tab[]
  activeTabKey: string | null
  /** Most-recently-used tab key history (most recent first). */
  mruOrder: string[]
  /** Per-tile active tab keys. */
  tileActiveTabKeys: Record<string, string | null>
  /** Per-tile MRU order. */
  tileMruOrder: Record<string, string[]>
}

/**
 * Subset of `TabStoreState` required to restore the tab store. Snapshots
 * captured from the live store include all fields; snapshots synthesized
 * for non-active workspaces may omit MRU/tile state (which `restore`
 * treats as empty).
 */
export type RestorableTabState
  = Pick<TabStoreState, 'tabs' | 'activeTabKey'>
    & Partial<Pick<TabStoreState, 'mruOrder' | 'tileActiveTabKeys' | 'tileMruOrder'>>

export interface AddTabOptions {
  activate?: boolean
  afterKey?: string | null
}

export function createTabStore() {
  const [state, setState] = createStore<TabStoreState>({
    tabs: [],
    activeTabKey: null,
    mruOrder: [],
    tileActiveTabKeys: {},
    tileMruOrder: {},
  })

  // Per-tile tab index. The render path filters tabs by tileId 3-4× per
  // Tile per render (tileAgentTabs / tileFileTabs / tileTerminals + the
  // TabBar list). Materializing the index in one pass turns those filters
  // into O(1) Map lookups. The memo tracks `state.tabs` and each tab's
  // `tileId` reactively, so any membership-changing mutation invalidates
  // it — no need to bookkeep refresh calls per mutation path.
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

  return {
    state,

    addTab(tab: Tab, options: AddTabOptions = {}) {
      const activate = options.activate ?? true
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
      const key = tabKey(tab)
      setState('tabs', prev => anchorIdx >= 0
        ? [...prev.slice(0, anchorIdx + 1), tab, ...prev.slice(anchorIdx + 1)]
        : [...prev, tab])
      if (activate) {
        setState('activeTabKey', key)
        setState('mruOrder', prev => [key, ...prev.filter(k => k !== key)])
      }
      else {
        // Still track in MRU (at end) so closing the active tab can fall back
        setState('mruOrder', prev => [...prev.filter(k => k !== key), key])
      }
      // Track in per-tile MRU if the tab has a tile
      if (tab.tileId) {
        if (activate) {
          setState('tileActiveTabKeys', tab.tileId, key)
          setState('tileMruOrder', tab.tileId, prev => [key, ...(prev ?? []).filter(k => k !== key)])
        }
        else {
          setState('tileMruOrder', tab.tileId, prev => [...(prev ?? []).filter(k => k !== key), key])
        }
      }
    },

    removeTab(type: TabType, id: string) {
      const key = tabKey({ type, id })
      const tab = tabsByKey().get(key)
      const tileId = tab?.tileId

      setState('tabs', prev => prev.filter(t => tabKey(t) !== key))
      setState('mruOrder', prev => prev.filter(k => k !== key))

      // Update per-tile state if the tab belonged to a tile
      if (tileId) {
        setState('tileMruOrder', tileId, prev => (prev ?? []).filter(k => k !== key))
        if (state.tileActiveTabKeys[tileId] === key) {
          const tileMru = state.tileMruOrder[tileId] ?? []
          const nextTileKey = tileMru[0] ?? null
          setState('tileActiveTabKeys', tileId, nextTileKey)
        }
      }

      // If the removed tab was active, activate the most recently used tab
      if (state.activeTabKey === key) {
        const nextKey = state.mruOrder[0] ?? null
        setState('activeTabKey', nextKey)
      }
    },

    setActiveTab(type: TabType, id: string) {
      const key = tabKey({ type, id })
      setState('activeTabKey', key)
      // Skip rewriting mruOrder when key is already at the head — a no-op
      // write still hands subscribers a new array reference.
      if (state.mruOrder[0] !== key)
        setState('mruOrder', prev => [key, ...prev.filter(k => k !== key)])
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

      // Calculate new LexoRank position. Index in `newTabs` (with the moved
      // tab already removed) needs to shift left by one when moving forward,
      // since the splice above shrunk the array.
      const insertIdx = fromIdx < toIdx ? toIdx - 1 : toIdx
      const newPosition = positionAtInsertIdx(newTabs, insertIdx)
      moved.position = newPosition

      newTabs.splice(insertIdx, 0, moved)
      setState('tabs', newTabs)
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
      setState('mruOrder', [])
      setState('tileActiveTabKeys', {})
      setState('tileMruOrder', {})
    },

    /** Snapshot the current state for registry caching. */
    snapshot(): TabStoreState {
      return {
        tabs: state.tabs.map(t => ({ ...t })),
        activeTabKey: state.activeTabKey,
        mruOrder: [...state.mruOrder],
        tileActiveTabKeys: { ...state.tileActiveTabKeys },
        tileMruOrder: Object.fromEntries(
          Object.entries(state.tileMruOrder).map(([k, v]) => [k, [...v]]),
        ),
      }
    },

    /** Restore from a previously snapshotted state. Missing MRU/tile fields initialize empty. */
    restore(snap: RestorableTabState) {
      setState({
        tabs: snap.tabs.map(t => ({ ...t })),
        activeTabKey: snap.activeTabKey,
        mruOrder: snap.mruOrder ? [...snap.mruOrder] : [],
        tileActiveTabKeys: snap.tileActiveTabKeys ? { ...snap.tileActiveTabKeys } : {},
        tileMruOrder: snap.tileMruOrder
          ? Object.fromEntries(Object.entries(snap.tileMruOrder).map(([k, v]) => [k, [...v]]))
          : {},
      })
    },

    /** Get tabs for a specific tile. O(1) via the `tabsByTile` memo. */
    getTabsForTile(tileId: string): Tab[] {
      return tabsByTile().get(tileId) ?? []
    },

    /** Get the active tab key for a specific tile. */
    getActiveTabKeyForTile(tileId: string): string | null {
      return state.tileActiveTabKeys[tileId] ?? null
    },

    /** Get the active tab object for a specific tile. O(1) via the `tabsByKey` memo. */
    getActiveTabForTile(tileId: string): Tab | null {
      const key = state.tileActiveTabKeys[tileId]
      if (!key)
        return null
      return tabsByKey().get(key) ?? null
    },

    /** Set the active tab for a specific tile. */
    setActiveTabForTile(tileId: string, type: TabType, id: string) {
      const key = tabKey({ type, id })
      setState('tileActiveTabKeys', tileId, key)
      // Skip rewriting tileMruOrder when key is already at the head — a
      // no-op write still hands subscribers a new array reference.
      if ((state.tileMruOrder[tileId] ?? [])[0] !== key) {
        setState('tileMruOrder', tileId, prev => [key, ...(prev ?? []).filter(k => k !== key)])
      }
      setState('tabs', t => tabKey(t) === key && !!t.hasNotification, 'hasNotification', false)
    },

    /** Set the position of a tab by key. */
    setTabPosition(key: string, position: string) {
      setState('tabs', t => tabKey(t) === key, 'position', position)
    },

    /** Set the display mode (render/source/split) for a file tab. */
    setTabDisplayMode(type: TabType, id: string, displayMode: string) {
      const key = tabKey({ type, id })
      setState('tabs', t => tabKey(t) === key, 'displayMode', displayMode)
    },

    /** Set the file view mode for a file tab. */
    setTabFileViewMode(type: TabType, id: string, mode: FileViewMode) {
      const key = tabKey({ type, id })
      setState('tabs', t => tabKey(t) === key, 'fileViewMode', mode)
    },

    /** Set the file diff base for a file tab. */
    setTabFileDiffBase(type: TabType, id: string, base: FileDiffBase) {
      const key = tabKey({ type, id })
      setState('tabs', t => tabKey(t) === key, 'fileDiffBase', base)
    },

    /** Update arbitrary fields on a tab. */
    updateTab(type: TabType, id: string, fields: Partial<Tab>) {
      const key = tabKey({ type, id })
      setState('tabs', t => tabKey(t) === key, prev => ({ ...prev, ...fields }))
    },

    /**
     * Apply the same `fields` to every tab matching `predicate` in a single
     * store mutation. Use this when an effect would otherwise call
     * `updateTab` in a loop — each call walks the tabs array, so batching is
     * O(N) instead of O(N·K) for K matches.
     */
    updateMatchingTabs(predicate: (tab: Tab) => boolean, fields: Partial<Tab>) {
      setState('tabs', predicate, prev => ({ ...prev, ...fields }))
    },

    /** Find a terminal tab by its terminal id. */
    getTerminalTab(id: string): Tab | undefined {
      return tabsByKey().get(tabKey({ type: TabType.TERMINAL, id }))
    },

    /** Find a tab by its `tabKey(...)` string. O(1) via the `tabsByKey` memo. */
    getTabByKey(key: string): Tab | undefined {
      return tabsByKey().get(key)
    },

    /** Downgrade all running terminal tabs on a worker to disconnected in a single pass. */
    markTerminalsDisconnected(workerId: string) {
      setState(
        'tabs',
        t => t.type === TabType.TERMINAL && t.workerId === workerId && t.status === TerminalStatus.READY,
        'status',
        TerminalStatus.DISCONNECTED,
      )
    },

    /** Mark a terminal tab as exited. No-op if the tab is missing or already exited. */
    markTerminalExited(id: string) {
      setState(
        'tabs',
        t => t.type === TabType.TERMINAL && t.id === id && t.status !== TerminalStatus.EXITED,
        'status',
        TerminalStatus.EXITED,
      )
    },

    /** Idempotently mark a terminal as having painted non-whitespace content. */
    markTerminalContentReady(id: string) {
      setState(
        'tabs',
        t => t.type === TabType.TERMINAL && t.id === id && !t.contentReady,
        'contentReady',
        true,
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
        'lastOffset',
        offset,
      )
    },

    /** For each tile that has tabs but no active tab, activate the first tab. */
    initMissingTileActiveTabs() {
      const tileIds = new Set(state.tabs.map(t => t.tileId).filter(Boolean) as string[])
      for (const tileId of tileIds) {
        if (!state.tileActiveTabKeys[tileId]) {
          const firstTab = state.tabs.find(t => t.tileId === tileId)
          if (firstTab) {
            const key = tabKey(firstTab)
            setState('tileActiveTabKeys', tileId, key)
            setState('tileMruOrder', tileId, prev => [key, ...(prev ?? []).filter(k => k !== key)])
          }
        }
      }
    },

    /** Move a tab to a different tile, cleaning up source tile state. */
    moveTabToTile(key: string, targetTileId: string) {
      // Find the tab's current tile before moving
      const sourceTileId = tabsByKey().get(key)?.tileId

      // Move the tab
      setState('tabs', t => tabKey(t) === key, 'tileId', targetTileId)

      // Clean up source tile state
      if (sourceTileId && sourceTileId !== targetTileId) {
        // Remove from source tile MRU
        setState('tileMruOrder', sourceTileId, prev => (prev ?? []).filter(k => k !== key))

        // If the moved tab was active in the source tile, fall back to MRU
        if (state.tileActiveTabKeys[sourceTileId] === key) {
          const tileMru = state.tileMruOrder[sourceTileId] ?? []
          const nextKey = tileMru[0] ?? null
          setState('tileActiveTabKeys', sourceTileId, nextKey)
        }
      }
    },

    /**
     * Reassign every tab whose tileId is in `oldTileIds` to `newTileId`,
     * merging their per-tile MRU/active state into the new tile and deleting
     * the source tiles' state. Used by the "Convert to tile" close-grid mode.
     */
    reassignTabsToTile(oldTileIds: string[], newTileId: string) {
      const oldSet = new Set(oldTileIds)
      // 1. Bulk-update tab.tileId.
      setState('tabs', tab => tab.tileId !== undefined && oldSet.has(tab.tileId), 'tileId', newTileId)

      // 2. MRU merge: concatenate in oldTileIds order, dedupe (first-occurrence wins).
      const seen = new Set<string>()
      const mergedMru: string[] = []
      for (const id of oldTileIds) {
        const list = state.tileMruOrder[id]
        if (!list)
          continue
        for (const k of list) {
          if (!seen.has(k)) {
            seen.add(k)
            mergedMru.push(k)
          }
        }
      }
      setState('tileMruOrder', newTileId, mergedMru)

      // 3. Active tab: first source tile in `oldTileIds` that has one wins.
      let mergedActive: string | null = null
      for (const id of oldTileIds) {
        const a = state.tileActiveTabKeys[id]
        if (a) {
          mergedActive = a
          break
        }
      }
      if (mergedActive == null && mergedMru.length > 0) {
        mergedActive = mergedMru[0]
      }
      setState('tileActiveTabKeys', newTileId, mergedActive)

      // 4. Cleanup of source tile state.
      this.cleanupTiles(oldTileIds.filter(id => id !== newTileId))
    },

    /**
     * Move every tab on `sourceTileId` onto `targetTileId`, appending to the
     * end of the target's tab list, and clean up the source tile's MRU/active
     * state. Differs from `reassignTabsToTile` (used for grid → tile merge):
     * the target here is an existing tile that already has its own active tab
     * and MRU, both of which are preserved.
     */
    mergeTabsIntoTile(sourceTileId: string, targetTileId: string) {
      if (sourceTileId === targetTileId)
        return
      const byTile = tabsByTile()
      const sourceTabs = byTile.get(sourceTileId) ?? []
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
            }
          }
        }))

        // Append source MRU to target's, deduped, without overwriting
        // target's active tab.
        const sourceMru = state.tileMruOrder[sourceTileId] ?? []
        if (sourceMru.length > 0) {
          setState('tileMruOrder', targetTileId, (prev) => {
            const existing = prev ?? []
            const seen = new Set(existing)
            const additions = sourceMru.filter(k => !seen.has(k))
            return [...existing, ...additions]
          })
        }
        // Adopt source's active only when the target had none.
        if (state.tileActiveTabKeys[targetTileId] == null) {
          const sourceActive = state.tileActiveTabKeys[sourceTileId] ?? null
          if (sourceActive)
            setState('tileActiveTabKeys', targetTileId, sourceActive)
        }
      }
      this.cleanupTile(sourceTileId)
    },

    /**
     * Drop per-tile MRU and active-tab entries for a removed tile. Tile ids
     * are minted from a monotonic counter and never reused, so without this
     * the records leak into every snapshot until workspace switch.
     */
    cleanupTile(tileId: string) {
      this.cleanupTiles([tileId])
    },

    /**
     * Bulk variant of `cleanupTile`: drops MRU/active-tab entries for every
     * tile id in `tileIds` using one `produce` per map. Callers closing a
     * grid or floating window otherwise issue 2 `setState` calls per tile —
     * the batched form fires each map's reactive notification at most once.
     */
    cleanupTiles(tileIds: Iterable<string>) {
      const ids = [...tileIds]
      if (ids.length === 0)
        return
      const mruIds = ids.filter(id => state.tileMruOrder[id] !== undefined)
      if (mruIds.length > 0) {
        setState('tileMruOrder', produce((m) => {
          for (const id of mruIds)
            delete m[id]
        }))
      }
      const activeIds = ids.filter(id => state.tileActiveTabKeys[id] !== undefined)
      if (activeIds.length > 0) {
        setState('tileActiveTabKeys', produce((m) => {
          for (const id of activeIds)
            delete m[id]
        }))
      }
    },

  }
}
