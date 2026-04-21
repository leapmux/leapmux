import type { listTerminals } from '~/api/workerRpc'
import type { AgentProvider } from '~/generated/leapmux/v1/agent_pb'
import { createStore } from 'solid-js/store'
import { TerminalStatus as TerminalStatusEnum } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { after, first, mid } from '~/lib/lexorank'

export { TabType }

export type FileViewMode = 'working' | 'head' | 'staged' | 'unified-diff' | 'split-diff'
export type FileDiffBase = 'head-vs-working' | 'head-vs-staged'
export type FileOpenSource = 'all' | 'changed' | 'staged' | 'unstaged'
/**
 * Frontend-only superset of the backend's TerminalStatus proto enum.
 * `'starting'`, `'running'`, and `'startup-failed'` map to the proto
 * values; `'disconnected'` and `'exited'` are client-derived lifecycle
 * states the backend doesn't carry. Keep as a string union rather than
 * converging on the proto enum so the UI can reason about all five.
 */
export type TerminalStatus = 'starting' | 'running' | 'disconnected' | 'exited' | 'startup-failed'

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
  cols?: number
  rows?: number
  /** Error string from TerminalStatusChange when status is 'startup-failed'. */
  startupError?: string
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
    case TerminalStatusEnum.STARTING:
      status = 'starting'
      break
    case TerminalStatusEnum.STARTUP_FAILED:
      status = 'startup-failed'
      break
    case TerminalStatusEnum.READY:
    default:
      status = term.exited ? 'exited' : 'running'
  }
  return {
    title: term.title || undefined,
    workerId,
    workingDir: term.workingDir || undefined,
    shellStartDir: term.shellStartDir || undefined,
    screen: term.screen.length > 0 ? term.screen : undefined,
    cols: term.cols || undefined,
    rows: term.rows || undefined,
    gitBranch: term.gitBranch || undefined,
    gitOriginUrl: term.gitOriginUrl || undefined,
    status,
    startupError: term.startupError || undefined,
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

export interface TabItemOps {
  onClose?: (tab: Tab) => void
  onRename?: (tab: Tab, title: string) => void
  closingKeys?: Set<string>
}

export function tabKey(tab: Tab): string {
  return `${tab.type}:${tab.id}`
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

  return {
    state,

    addTab(tab: Tab, options: AddTabOptions = {}) {
      const activate = options.activate ?? true
      const anchorIdx = options.afterKey
        ? state.tabs.findIndex(t => tabKey(t) === options.afterKey)
        : -1

      if (!tab.position) {
        if (anchorIdx >= 0) {
          const anchorTab = state.tabs[anchorIdx]
          const nextTab = state.tabs[anchorIdx + 1]
          const prevPos = anchorTab.position
          const nextPos = nextTab?.position ?? ''
          tab = {
            ...tab,
            position: nextPos ? mid(prevPos ?? '', nextPos) : prevPos ? after(prevPos) : first(),
          }
        }
        else {
          const lastTab = state.tabs.at(-1)
          tab = { ...tab, position: lastTab?.position ? after(lastTab.position) : first() }
        }
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
      const tab = state.tabs.find(t => tabKey(t) === key)
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
      setState('mruOrder', prev => [key, ...prev.filter(k => k !== key)])
      // Clear notification on the newly active tab
      setState('tabs', t => tabKey(t) === key, 'hasNotification', false)
    },

    activeTab(): Tab | null {
      const key = state.activeTabKey
      if (!key)
        return null
      return state.tabs.find(t => tabKey(t) === key) ?? null
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

      // Calculate new LexoRank position
      const insertIdx = fromIdx < toIdx ? toIdx - 1 : toIdx
      const prevPos = insertIdx > 0 ? newTabs[insertIdx - 1]?.position ?? '' : ''
      const nextPos = insertIdx < newTabs.length ? newTabs[insertIdx]?.position ?? '' : ''
      const newPosition = mid(prevPos, nextPos)
      moved.position = newPosition

      newTabs.splice(toIdx > fromIdx ? toIdx - 1 + 1 : toIdx, 0, moved)
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

    /** Get tabs for a specific tile. */
    getTabsForTile(tileId: string): Tab[] {
      return state.tabs.filter(t => t.tileId === tileId)
    },

    /** Get the active tab key for a specific tile. */
    getActiveTabKeyForTile(tileId: string): string | null {
      return state.tileActiveTabKeys[tileId] ?? null
    },

    /** Get the active tab object for a specific tile. */
    getActiveTabForTile(tileId: string): Tab | null {
      const key = state.tileActiveTabKeys[tileId]
      if (!key)
        return null
      return state.tabs.find(t => tabKey(t) === key) ?? null
    },

    /** Set the active tab for a specific tile. */
    setActiveTabForTile(tileId: string, type: TabType, id: string) {
      const key = tabKey({ type, id })
      setState('tileActiveTabKeys', tileId, key)
      setState('tileMruOrder', tileId, prev => [key, ...(prev ?? []).filter(k => k !== key)])
      setState('tabs', t => tabKey(t) === key, 'hasNotification', false)
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

    /** Find a terminal tab by its terminal id. */
    getTerminalTab(id: string): Tab | undefined {
      return state.tabs.find(t => t.type === TabType.TERMINAL && t.id === id)
    },

    /** Downgrade all running terminal tabs on a worker to disconnected in a single pass. */
    markTerminalsDisconnected(workerId: string) {
      setState(
        'tabs',
        t => t.type === TabType.TERMINAL && t.workerId === workerId && t.status === 'running',
        'status',
        'disconnected',
      )
    },

    /** Mark a terminal tab as exited. No-op if the tab is missing or already exited. */
    markTerminalExited(id: string) {
      setState(
        'tabs',
        t => t.type === TabType.TERMINAL && t.id === id && t.status !== 'exited',
        'status',
        'exited',
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
      const tab = state.tabs.find(t => tabKey(t) === key)
      const sourceTileId = tab?.tileId

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

    /** Remove a tab and update per-tile state. */
    removeTabFromTile(type: TabType, id: string, tileId: string) {
      const key = tabKey({ type, id })
      setState('tabs', prev => prev.filter(t => tabKey(t) !== key))
      setState('mruOrder', prev => prev.filter(k => k !== key))
      setState('tileMruOrder', tileId, prev => (prev ?? []).filter(k => k !== key))

      // If removed tab was active in the tile, activate MRU for that tile
      if (state.tileActiveTabKeys[tileId] === key) {
        const tileMru = state.tileMruOrder[tileId] ?? []
        const nextKey = tileMru[0] ?? null
        setState('tileActiveTabKeys', tileId, nextKey)
      }

      // Also update global active tab if needed
      if (state.activeTabKey === key) {
        const nextKey = state.mruOrder[0] ?? null
        setState('activeTabKey', nextKey)
      }
    },
  }
}
