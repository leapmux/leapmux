import type { WorkspaceSnapshot } from '~/stores/workspaceStoreRegistry'
import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SIDEBAR_TAB_PREFIX } from '~/components/shell/TabDragContext'
import { useCrossWorkspaceMove } from '~/components/shell/useCrossWorkspaceMove'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabStore } from '~/stores/tab.store'
import { isFileTab } from '~/stores/tab.types'
import { createWorkspaceStoreRegistry } from '~/stores/workspaceStoreRegistry'
import { flush } from '../helpers/async'
import { installTestBridge } from '../helpers/crdtBridge'

// The cross-workspace move issues a worker RPC (then a CRDT batch) -- stub the RPCs so
// these tests assert the OPTIMISTIC local move (the part the user sees immediately) and
// which RPC fired, without a live worker. Only useCrossWorkspaceMove consumes these
// modules in this graph, so a scoped replacement is safe.
const mockMoveTabWorkspace = vi.fn((..._args: unknown[]) => Promise.resolve({}))
const mockRelocateFileTabPath = vi.fn((..._args: unknown[]) => Promise.resolve({}))
const mockListTabsForWorkspace = vi.fn((..._args: unknown[]) => Promise.resolve({ tabs: [] }))

vi.mock('~/api/workerRpc', () => ({
  moveTabWorkspace: (...args: unknown[]) => mockMoveTabWorkspace(...args),
  relocateFileTabPath: (...args: unknown[]) => mockRelocateFileTabPath(...args),
}))
vi.mock('~/api/listTabsBatcher', () => ({
  listTabsForWorkspace: (...args: unknown[]) => mockListTabsForWorkspace(...args),
}))
vi.mock('~/components/common/Toast', () => ({
  showWarnToast: vi.fn(),
  showErrorToast: vi.fn(),
  showInfoToast: vi.fn(),
}))

/** Tab key as the store builds it: `${type}:${id}` (type is the numeric TabType enum). */
function key(type: TabType, id: string): string {
  return `${type}:${id}`
}

/** A flat WorkspaceSnapshot (the current shape: `tabs` is a bare Tab[], no agents slot). */
function makeSnapshot(workspaceId: string, overrides?: Partial<WorkspaceSnapshot>): WorkspaceSnapshot {
  return {
    workspaceId,
    tabs: [],
    activeTabKey: null,
    layout: { root: { type: 'leaf', id: 'tile-1' }, focusedTileId: 'tile-1' },
    restored: true,
    tabsLoaded: true,
    ...overrides,
  }
}

/** Stand up the real stores + the real move handler over them. */
function setup(activeWsId = 'ws-active') {
  installTestBridge({ rootTileId: 'tile-1' })
  const tabStore = createTabStore()
  const layoutStore = createLayoutStore()
  layoutStore.setFocusedTile('tile-1')
  const floatingWindowStore = createFloatingWindowStore()
  const registry = createWorkspaceStoreRegistry()
  const focusTile = vi.fn()
  const { move } = useCrossWorkspaceMove({
    getActiveWorkspaceId: () => activeWsId,
    getOrgId: () => 'org-1',
    tabStore,
    layoutStore,
    floatingWindowStore,
    registry,
    pendingMgr: () => null,
    batchResultHandlers: new Map(),
    focusTile,
  })
  return { activeWsId, tabStore, layoutStore, floatingWindowStore, registry, focusTile, move }
}

describe('useCrossWorkspaceMove', () => {
  afterEach(() => {
    vi.clearAllMocks()
  })

  it('moves an active-workspace tab into a cached target snapshot on the target tile', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.tabStore.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1', workerId: 'w1' })
      h.registry.set('ws-target', makeSnapshot('ws-target', {
        layout: { root: { type: 'leaf', id: 'target-tile' }, focusedTileId: 'target-tile' },
      }))

      h.move('ws-target', key(TabType.AGENT, 'a1'))

      // The active store lost the tab; the target snapshot gained it, landing on the
      // target workspace's own focused tile and becoming its active tab.
      expect(h.tabStore.getTabByKey(key(TabType.AGENT, 'a1'))).toBeUndefined()
      const target = h.registry.get('ws-target')!
      expect(target.tabs.map(t => t.id)).toEqual(['a1'])
      expect(target.tabs[0].tileId).toBe('target-tile')
      expect(target.activeTabKey).toBe(key(TabType.AGENT, 'a1'))
      expect(target.tileActiveTabKeys?.['target-tile']).toBe(key(TabType.AGENT, 'a1'))
      // Worker bookkeeping flips first, via MoveTabWorkspace for an AGENT tab.
      expect(mockMoveTabWorkspace).toHaveBeenCalledWith('w1', expect.objectContaining({
        tabType: TabType.AGENT,
        tabId: 'a1',
        newWorkspaceId: 'ws-target',
      }))
      await flush()
      dispose()
    })
  })

  it('carries agent metadata (workerId, title) on the moved tab record -- no separate agents slot', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.tabStore.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1', workerId: 'w1', title: 'Agent Olivia' })
      h.registry.set('ws-target', makeSnapshot('ws-target'))

      h.move('ws-target', key(TabType.AGENT, 'a1'))

      const moved = h.registry.get('ws-target')!.tabs[0]
      expect(moved.workerId).toBe('w1')
      expect(moved.title).toBe('Agent Olivia')
      await flush()
      dispose()
    })
  })

  it('moves a FILE tab via the relocate RPC, preserving its path and display mode', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.tabStore.addTab({ type: TabType.FILE, id: 'f1', tileId: 'tile-1', workerId: 'w1', filePath: '/home/user/readme.md', title: 'readme.md' })
      h.tabStore.setTabDisplayMode(TabType.FILE, 'f1', 'split')
      h.registry.set('ws-target', makeSnapshot('ws-target'))

      h.move('ws-target', key(TabType.FILE, 'f1'))

      const moved = h.registry.get('ws-target')!.tabs[0]
      expect(isFileTab(moved) && moved.filePath).toBe('/home/user/readme.md')
      expect(isFileTab(moved) && moved.displayMode).toBe('split')
      expect(moved.title).toBe('readme.md')
      // FILE tabs relocate via the E2EE path (RelocateFileTabPath), NOT MoveTabWorkspace.
      expect(mockRelocateFileTabPath).toHaveBeenCalledWith('w1', expect.objectContaining({
        tabId: 'f1',
        newWorkspaceId: 'ws-target',
      }))
      expect(mockMoveTabWorkspace).not.toHaveBeenCalled()
      await flush()
      dispose()
    })
  })

  it('moves a tab between two non-active workspace snapshots', async () => {
    await createRoot(async (dispose) => {
      const h = setup() // active = ws-active; both source and target are non-active
      h.registry.set('ws-source', makeSnapshot('ws-source', {
        tabs: [{ type: TabType.AGENT, id: 'a1', position: 'a', tileId: 'tile-1', workerId: 'w1' }],
        activeTabKey: key(TabType.AGENT, 'a1'),
      }))
      h.registry.set('ws-target', makeSnapshot('ws-target'))

      h.move('ws-target', key(TabType.AGENT, 'a1'), 'ws-source')

      expect(h.registry.get('ws-source')!.tabs).toEqual([])
      expect(h.registry.get('ws-target')!.tabs.map(t => t.id)).toEqual(['a1'])
      await flush()
      dispose()
    })
  })

  it('moves a non-active snapshot tab into the active workspace and focuses its tile', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.registry.set('ws-source', makeSnapshot('ws-source', {
        tabs: [{ type: TabType.AGENT, id: 'a1', position: 'a', tileId: 'src-tile', workerId: 'w1' }],
      }))

      // Target is the active workspace; drop onto tile-1.
      h.move(h.activeWsId, key(TabType.AGENT, 'a1'), 'ws-source', 'tile-1')

      const active = h.tabStore.getTabByKey(key(TabType.AGENT, 'a1'))
      expect(active?.tileId).toBe('tile-1')
      expect(h.registry.get('ws-source')!.tabs).toEqual([])
      expect(h.focusTile).toHaveBeenCalledWith('tile-1')
      await flush()
      dispose()
    })
  })

  it('is a no-op when source and target resolve to the same workspace', async () => {
    await createRoot((dispose) => {
      const h = setup()
      h.tabStore.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })

      // Target === the active source workspace -> the guard returns early.
      h.move(h.activeWsId, key(TabType.AGENT, 'a1'))

      expect(h.tabStore.getTabByKey(key(TabType.AGENT, 'a1'))).toBeDefined()
      expect(mockMoveTabWorkspace).not.toHaveBeenCalled()
      dispose()
    })
  })

  it('appends to an existing target snapshot rather than replacing its tabs', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.registry.set('ws-target', makeSnapshot('ws-target', {
        tabs: [{ type: TabType.AGENT, id: 'existing', position: 'a', tileId: 'tile-1' }],
        activeTabKey: key(TabType.AGENT, 'existing'),
      }))
      h.tabStore.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-1' })

      h.move('ws-target', key(TabType.TERMINAL, 't1'))

      expect(h.registry.get('ws-target')!.tabs.map(t => t.id)).toEqual(['existing', 't1'])
      await flush()
      dispose()
    })
  })

  it('creates a fresh, not-yet-loaded snapshot when the target workspace was never opened', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.tabStore.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1', workerId: 'w1' })
      expect(h.registry.get('ws-new')).toBeUndefined()

      h.move('ws-new', key(TabType.AGENT, 'a1'), undefined, 'new-tile')

      // Read the optimistic snapshot synchronously, BEFORE the async ListTabs merge runs.
      const created = h.registry.get('ws-new')
      expect(created?.tabs.map(t => t.id)).toEqual(['a1'])
      // tabsLoaded:false marks it for the post-move ListTabs fetch that fills the hub's
      // existing tabs before the user switches in.
      expect(created?.tabsLoaded).toBe(false)
      await flush() // drain the async ListTabs merge (mocked empty)
      dispose()
    })
  })

  it('restores the moved tab when the target snapshot is later restored into the store', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.registry.set('ws-a', makeSnapshot('ws-a'))
      h.registry.set('ws-b', makeSnapshot('ws-b', {
        tabs: [{ type: TabType.AGENT, id: 'a1', position: 'a', tileId: 'tile-1', workerId: 'w1' }],
      }))

      // Move a1 from ws-b (non-active) to ws-a (non-active), then restore ws-a's snapshot.
      h.move('ws-a', key(TabType.AGENT, 'a1'), 'ws-b', 'tile-1')
      h.tabStore.restore(h.registry.get('ws-a')!)

      expect(h.tabStore.state.tabs.map(t => t.id)).toEqual(['a1'])
      expect(h.tabStore.state.tabs[0].tileId).toBe('tile-1')
      expect(h.tabStore.state.activeTabKey).toBe(key(TabType.AGENT, 'a1'))
      await flush()
      dispose()
    })
  })

  it('falls the source tile back to its next MRU tab when the active tab is moved out', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.tabStore.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      h.tabStore.setActiveTabForTile('tile-1', TabType.AGENT, 'a1')
      h.tabStore.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-1' })
      h.tabStore.setActiveTabForTile('tile-1', TabType.TERMINAL, 't1')
      h.tabStore.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-1' })
      h.tabStore.setActiveTabForTile('tile-1', TabType.AGENT, 'a2') // per-tile MRU: [a2, t1, a1]; active a2
      h.registry.set('ws-target', makeSnapshot('ws-target'))

      h.move('ws-target', key(TabType.AGENT, 'a2'))

      // a2 left the active store; the source tile's active falls back to the next MRU (t1).
      expect(h.tabStore.getTabByKey(key(TabType.AGENT, 'a2'))).toBeUndefined()
      expect(h.tabStore.getActiveTabKeyForTile('tile-1')).toBe(key(TabType.TERMINAL, 't1'))
      await flush()
      dispose()
    })
  })
})

describe('sidebar tab drag ID parsing', () => {
  it('should correctly encode sidebar tab draggable ID', () => {
    const wsId = 'ws-123'
    const tabType = TabType.AGENT
    const tabId = 'agent-456'
    const id = `${SIDEBAR_TAB_PREFIX}${wsId}:${tabType}:${tabId}`

    expect(id).toBe('sidebar-tab:ws-123:1:agent-456')
    expect(id.startsWith(SIDEBAR_TAB_PREFIX)).toBe(true)
  })

  it('should parse workspace ID and tab key from sidebar tab ID', () => {
    const id = `${SIDEBAR_TAB_PREFIX}ws-abc:${TabType.TERMINAL}:term-def`
    const rest = id.slice(SIDEBAR_TAB_PREFIX.length)
    const colonIdx = rest.indexOf(':')
    const wsId = rest.slice(0, colonIdx)
    const realTabKey = rest.slice(colonIdx + 1)

    expect(wsId).toBe('ws-abc')
    expect(realTabKey).toBe(`${TabType.TERMINAL}:term-def`)
  })

  it('should distinguish sidebar tab drags from tabbar tab drags', () => {
    const sidebarId = `${SIDEBAR_TAB_PREFIX}ws-1:1:agent-1`
    const tabbarId = '1:agent-1'

    expect(sidebarId.startsWith(SIDEBAR_TAB_PREFIX)).toBe(true)
    expect(tabbarId.startsWith(SIDEBAR_TAB_PREFIX)).toBe(false)
    expect(sidebarId.startsWith('ws-')).toBe(false) // should not be confused with workspace drag
  })
})
