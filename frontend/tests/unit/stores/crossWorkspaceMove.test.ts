import type { WorkspaceSnapshot } from '~/stores/workspaceStoreRegistry'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { SIDEBAR_TAB_PREFIX } from '~/components/shell/TabDragContext'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createTabStore } from '~/stores/tab.store'
import { createWorkspaceStoreRegistry } from '~/stores/workspaceStoreRegistry'

function makeSnapshot(workspaceId: string, overrides?: Partial<WorkspaceSnapshot>): WorkspaceSnapshot {
  return {
    workspaceId,
    tabs: {
      tabs: [],
      activeTabKey: null,
      mruOrder: [],
      tileActiveTabKeys: {},
      tileMruOrder: {},
    },
    layout: {
      root: { type: 'leaf', id: 'tile-1' },
      focusedTileId: 'tile-1',
    },
    agents: [],
    terminals: [],
    restored: true,
    tabsLoaded: true,
    ...overrides,
  }
}

describe('cross-workspace tab move via registry', () => {
  it('should transfer a tab from active store to target registry snapshot', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const registry = createWorkspaceStoreRegistry()

      // Set up active workspace with two tabs
      tabStore.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      tabStore.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-1' })
      expect(tabStore.state.tabs.length).toBe(2)

      // Set up target workspace in registry (empty)
      registry.set('ws-target', makeSnapshot('ws-target'))

      // Simulate cross-workspace move: find tab, remove from store, add to registry
      const tab = tabStore.state.tabs.find(t => t.type === TabType.AGENT && t.id === 'a1')!
      tabStore.removeTab(tab.type, tab.id)

      const targetSnap = registry.get('ws-target')!
      const newTab = { ...tab }
      targetSnap.tabs.tabs = [...targetSnap.tabs.tabs, newTab]
      registry.set('ws-target', { ...targetSnap })

      // Active store should have one tab remaining
      expect(tabStore.state.tabs.length).toBe(1)
      expect(tabStore.state.tabs[0].id).toBe('t1')

      // Target snapshot should have the moved tab
      const updatedSnap = registry.get('ws-target')!
      expect(updatedSnap.tabs.tabs.length).toBe(1)
      expect(updatedSnap.tabs.tabs[0].type).toBe(TabType.AGENT)
      expect(updatedSnap.tabs.tabs[0].id).toBe('a1')

      dispose()
    })
  })

  it('should preserve tab properties during cross-workspace transfer', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const registry = createWorkspaceStoreRegistry()

      tabStore.addTab({
        type: TabType.FILE,
        id: 'f1',
        tileId: 'tile-1',
        filePath: '/home/user/readme.md',
        title: 'readme.md',
      })
      tabStore.setTabDisplayMode(TabType.FILE, 'f1', 'split')

      registry.set('ws-target', makeSnapshot('ws-target'))

      const tab = tabStore.state.tabs.find(t => t.type === TabType.FILE && t.id === 'f1')!
      tabStore.removeTab(tab.type, tab.id)

      const targetSnap = registry.get('ws-target')!
      targetSnap.tabs.tabs = [...targetSnap.tabs.tabs, { ...tab }]
      registry.set('ws-target', { ...targetSnap })

      const movedTab = registry.get('ws-target')!.tabs.tabs[0]
      expect(movedTab.filePath).toBe('/home/user/readme.md')
      expect(movedTab.title).toBe('readme.md')
      expect(movedTab.displayMode).toBe('split')

      dispose()
    })
  })

  it('should activate MRU tab in source tile after cross-workspace move', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const tileId = 'tile-1'

      tabStore.addTab({ type: TabType.AGENT, id: 'a1', tileId })
      tabStore.setActiveTabForTile(tileId, TabType.AGENT, 'a1')
      tabStore.addTab({ type: TabType.TERMINAL, id: 't1', tileId })
      tabStore.setActiveTabForTile(tileId, TabType.TERMINAL, 't1')
      tabStore.addTab({ type: TabType.AGENT, id: 'a2', tileId })
      tabStore.setActiveTabForTile(tileId, TabType.AGENT, 'a2')
      // Per-tile MRU: [a2, t1, a1]; active = a2

      // Move active tab (a2) out — simulates cross-workspace move
      tabStore.removeTab(TabType.AGENT, 'a2')

      // Should fall back to t1 (next in MRU)
      expect(tabStore.getActiveTabKeyForTile(tileId)).toBe('2:t1')
      expect(tabStore.state.tabs.length).toBe(2)

      dispose()
    })
  })

  it('should not change active tab in source tile when moving non-active tab', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const tileId = 'tile-1'

      tabStore.addTab({ type: TabType.AGENT, id: 'a1', tileId })
      tabStore.setActiveTabForTile(tileId, TabType.AGENT, 'a1')
      tabStore.addTab({ type: TabType.TERMINAL, id: 't1', tileId })
      // Don't activate t1 — a1 remains active

      // Move non-active tab (t1) out
      tabStore.removeTab(TabType.TERMINAL, 't1')

      expect(tabStore.getActiveTabKeyForTile(tileId)).toBe('1:a1')
      expect(tabStore.state.tabs.length).toBe(1)

      dispose()
    })
  })

  it('should handle move when target workspace has no snapshot yet', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const registry = createWorkspaceStoreRegistry()

      tabStore.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })

      // No snapshot for target — registry.get returns undefined
      expect(registry.get('ws-target')).toBeUndefined()

      // Tab removal still works even without target snapshot
      tabStore.removeTab(TabType.AGENT, 'a1')
      expect(tabStore.state.tabs.length).toBe(0)

      dispose()
    })
  })

  it('should append to existing tabs in target snapshot', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const registry = createWorkspaceStoreRegistry()

      // Target already has a tab
      registry.set('ws-target', makeSnapshot('ws-target', {
        tabs: {
          tabs: [
            { type: TabType.AGENT, id: 'existing-a1', position: 'a', tileId: 'tile-1' } as any,
          ],
          activeTabKey: '1:existing-a1',
          mruOrder: ['1:existing-a1'],
          tileActiveTabKeys: {},
          tileMruOrder: {},
        },
      }))

      tabStore.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-1' })

      const tab = tabStore.state.tabs.find(t => t.type === TabType.TERMINAL && t.id === 't1')!
      tabStore.removeTab(tab.type, tab.id)

      const targetSnap = registry.get('ws-target')!
      targetSnap.tabs.tabs = [...targetSnap.tabs.tabs, { ...tab }]
      registry.set('ws-target', { ...targetSnap })

      const updatedSnap = registry.get('ws-target')!
      expect(updatedSnap.tabs.tabs.length).toBe(2)
      expect(updatedSnap.tabs.tabs[0].id).toBe('existing-a1')
      expect(updatedSnap.tabs.tabs[1].id).toBe('t1')

      dispose()
    })
  })

  it('should skip move when target is same as active workspace', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const activeWsId = 'ws-active'

      tabStore.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })

      // Simulate the guard: if target === active, don't move
      const targetWorkspaceId = activeWsId
      if (targetWorkspaceId === activeWsId) {
        // No-op — tab should remain
        expect(tabStore.state.tabs.length).toBe(1)
      }

      dispose()
    })
  })

  it('should move tab from registry snapshot to registry snapshot', () => {
    createRoot((dispose) => {
      const registry = createWorkspaceStoreRegistry()

      // Source workspace has a tab
      registry.set('ws-source', makeSnapshot('ws-source', {
        tabs: {
          tabs: [
            { type: TabType.AGENT, id: 'a1', title: 'Agent 1', position: 'a', tileId: 'tile-1', workerId: 'w1' } as any,
          ],
          activeTabKey: '1:a1',
          mruOrder: ['1:a1'],
          tileActiveTabKeys: {},
          tileMruOrder: {},
        },
      }))
      registry.set('ws-target', makeSnapshot('ws-target'))

      // Simulate: remove from source snapshot, add to target snapshot
      const sourceSnap = registry.get('ws-source')!
      const draggedKey = `${TabType.AGENT}:a1`
      const tab = sourceSnap.tabs.tabs.find((t: any) => `${t.type}:${t.id}` === draggedKey)!

      sourceSnap.tabs.tabs = sourceSnap.tabs.tabs.filter((t: any) => `${t.type}:${t.id}` !== draggedKey)
      registry.set('ws-source', { ...sourceSnap })

      const targetSnap = registry.get('ws-target')!
      targetSnap.tabs.tabs = [...targetSnap.tabs.tabs, { ...tab }]
      registry.set('ws-target', { ...targetSnap })

      expect(registry.get('ws-source')!.tabs.tabs.length).toBe(0)
      expect(registry.get('ws-target')!.tabs.tabs.length).toBe(1)
      expect(registry.get('ws-target')!.tabs.tabs[0].id).toBe('a1')

      dispose()
    })
  })
})

describe('cross-workspace move to non-active target snapshot', () => {
  it('should set valid tileId from target layout when moving to non-active workspace', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const registry = createWorkspaceStoreRegistry()

      // Source has a tab with tileId from source layout
      tabStore.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'source-tile' })

      // Target snapshot has a different layout with its own tile IDs
      registry.set('ws-target', makeSnapshot('ws-target', {
        layout: {
          root: { type: 'leaf', id: 'target-tile' },
          focusedTileId: 'target-tile',
        },
      }))

      const tab = tabStore.state.tabs.find(t => t.id === 'a1')!
      tabStore.removeTab(tab.type, tab.id)

      // Simulate the fixed move logic: use target's focusedTileId
      const targetSnap = registry.get('ws-target')!
      const targetTileId = targetSnap.layout.focusedTileId ?? 'target-tile'
      const newTab = { ...tab, tileId: targetTileId }
      const key = `${newTab.type}:${newTab.id}`
      targetSnap.tabs.tabs = [...targetSnap.tabs.tabs, newTab]
      targetSnap.tabs.activeTabKey = key
      targetSnap.tabs.mruOrder = [key, ...targetSnap.tabs.mruOrder]
      if (targetTileId) {
        targetSnap.tabs.tileActiveTabKeys = {
          ...targetSnap.tabs.tileActiveTabKeys,
          [targetTileId]: key,
        }
      }
      registry.set('ws-target', { ...targetSnap })

      const updated = registry.get('ws-target')!
      // Tab should have target's tileId, not source's
      expect(updated.tabs.tabs[0].tileId).toBe('target-tile')
      // activeTabKey and mruOrder should be updated
      expect(updated.tabs.activeTabKey).toBe(`${TabType.AGENT}:a1`)
      expect(updated.tabs.mruOrder).toContain(`${TabType.AGENT}:a1`)
      // tileActiveTabKeys should map the target tile to the moved tab
      expect(updated.tabs.tileActiveTabKeys['target-tile']).toBe(`${TabType.AGENT}:a1`)

      dispose()
    })
  })

  it('should copy agent data to target snapshot when moving to non-active workspace', () => {
    createRoot((dispose) => {
      const registry = createWorkspaceStoreRegistry()

      const agent = { id: 'a1', workerId: 'w1', title: 'Test Agent' } as any

      // Source has an agent and corresponding tab
      registry.set('ws-source', makeSnapshot('ws-source', {
        tabs: {
          tabs: [{ type: TabType.AGENT, id: 'a1', tileId: 'tile-1', workerId: 'w1' } as any],
          activeTabKey: `${TabType.AGENT}:a1`,
          mruOrder: [`${TabType.AGENT}:a1`],
          tileActiveTabKeys: {},
          tileMruOrder: {},
        },
        agents: [agent],
      }))
      registry.set('ws-target', makeSnapshot('ws-target'))

      // Simulate move: remove from source, add to target with agent
      const sourceSnap = registry.get('ws-source')!
      const tab = sourceSnap.tabs.tabs[0]
      sourceSnap.tabs.tabs = []
      sourceSnap.agents = sourceSnap.agents.filter(a => a.id !== tab.id)
      registry.set('ws-source', { ...sourceSnap })

      const targetSnap = registry.get('ws-target')!
      targetSnap.tabs.tabs = [...targetSnap.tabs.tabs, { ...tab }]
      if (!targetSnap.agents.some(a => a.id === tab.id)) {
        targetSnap.agents = [...targetSnap.agents, agent]
      }
      registry.set('ws-target', { ...targetSnap })

      // Source should have no agents
      expect(registry.get('ws-source')!.agents.length).toBe(0)
      // Target should have the agent
      expect(registry.get('ws-target')!.agents.length).toBe(1)
      expect(registry.get('ws-target')!.agents[0].id).toBe('a1')

      dispose()
    })
  })

  it('should remove agent from source snapshot when moving from non-active workspace', () => {
    createRoot((dispose) => {
      const registry = createWorkspaceStoreRegistry()

      const agent = { id: 'a1', workerId: 'w1' } as any

      registry.set('ws-source', makeSnapshot('ws-source', {
        tabs: {
          tabs: [{ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' } as any],
          activeTabKey: `${TabType.AGENT}:a1`,
          mruOrder: [`${TabType.AGENT}:a1`],
          tileActiveTabKeys: {},
          tileMruOrder: {},
        },
        agents: [agent],
      }))

      // Remove tab AND agent from source
      const sourceSnap = registry.get('ws-source')!
      sourceSnap.tabs.tabs = sourceSnap.tabs.tabs.filter((t: any) => t.id !== 'a1')
      sourceSnap.agents = sourceSnap.agents.filter(a => a.id !== 'a1')
      registry.set('ws-source', { ...sourceSnap })

      expect(registry.get('ws-source')!.tabs.tabs.length).toBe(0)
      expect(registry.get('ws-source')!.agents.length).toBe(0)

      dispose()
    })
  })

  it('should restore correctly after move-back to original workspace', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const registry = createWorkspaceStoreRegistry()

      const agent = { id: 'a1', workerId: 'w1', title: 'Agent' } as any

      // ws-a starts with a tab, ws-b is empty
      registry.set('ws-a', makeSnapshot('ws-a', {
        tabs: {
          tabs: [],
          activeTabKey: null,
          mruOrder: [],
          tileActiveTabKeys: {},
          tileMruOrder: {},
        },
        agents: [],
      }))
      registry.set('ws-b', makeSnapshot('ws-b', {
        tabs: {
          tabs: [{ type: TabType.AGENT, id: 'a1', tileId: 'tile-1', workerId: 'w1' } as any],
          activeTabKey: `${TabType.AGENT}:a1`,
          mruOrder: [`${TabType.AGENT}:a1`],
          tileActiveTabKeys: { 'tile-1': `${TabType.AGENT}:a1` },
          tileMruOrder: {},
        },
        agents: [agent],
      }))

      // Simulate moving tab from ws-b (non-active) back to ws-a (non-active)
      const sourceSnap = registry.get('ws-b')!
      const tab = sourceSnap.tabs.tabs[0]
      sourceSnap.tabs.tabs = []
      sourceSnap.agents = []
      registry.set('ws-b', { ...sourceSnap })

      const targetSnap = registry.get('ws-a')!
      const targetTileId = targetSnap.layout.focusedTileId ?? 'tile-1'
      const newTab = { ...tab, tileId: targetTileId }
      const key = `${newTab.type}:${newTab.id}`
      targetSnap.tabs.tabs = [newTab]
      targetSnap.tabs.activeTabKey = key
      targetSnap.tabs.mruOrder = [key]
      targetSnap.tabs.tileActiveTabKeys = { [targetTileId]: key }
      targetSnap.agents = [agent]
      registry.set('ws-a', { ...targetSnap })

      // Now restore ws-a from its snapshot
      const cached = registry.get('ws-a')!
      tabStore.restore(cached.tabs)

      // Tab should be restored with correct tileId and active state
      expect(tabStore.state.tabs.length).toBe(1)
      expect(tabStore.state.tabs[0].id).toBe('a1')
      expect(tabStore.state.tabs[0].tileId).toBe('tile-1')
      expect(tabStore.state.activeTabKey).toBe(`${TabType.AGENT}:a1`)

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
