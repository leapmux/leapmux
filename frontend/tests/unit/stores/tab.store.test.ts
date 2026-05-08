import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import type { Tab } from '~/stores/tab.store'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import {
  createTabStore,
  isTabReadyForGitStatus,
  preserveTerminalDisplayFields,
  protoToTerminalTabFields,
  tabKey,
} from '~/stores/tab.store'

describe('tabKey', () => {
  it('should create composite key from type and id', () => {
    expect(tabKey({ type: 'agent', id: 'a1' })).toBe('agent:a1')
    expect(tabKey({ type: 'terminal', id: 't1' })).toBe('terminal:t1')
  })
})

describe('createTabStore', () => {
  it('should initialize with empty state', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      expect(store.state.tabs).toEqual([])
      expect(store.state.activeTabKey).toBeNull()
      expect(store.activeTab()).toBeNull()
      dispose()
    })
  })

  it('should add a tab and set it active', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: 'agent', id: 'a1' })
      expect(store.state.tabs).toMatchObject([{ type: 'agent', id: 'a1' }])
      expect(store.state.activeTabKey).toBe('agent:a1')
      expect(store.activeTab()).toMatchObject({ type: 'agent', id: 'a1' })
      dispose()
    })
  })

  it('should add multiple tabs and activate the latest', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: 'agent', id: 'a1' })
      store.addTab({ type: 'terminal', id: 't1' })
      store.addTab({ type: 'agent', id: 'a2' })
      expect(store.state.tabs.length).toBe(3)
      expect(store.state.activeTabKey).toBe('agent:a2')
      dispose()
    })
  })

  it('should insert a new tab immediately after the anchor tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-1' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-1' })

      store.addTab({ type: TabType.FILE, id: 'f1', tileId: 'tile-1' }, { afterKey: '2:t1' })

      expect(store.state.tabs.map(tabKey)).toEqual(['1:a1', '2:t1', '3:f1', '1:a2'])
      expect(store.state.tabs[2].position).toBeTruthy()
      expect(store.state.tabs[1].position! < store.state.tabs[2].position!).toBe(true)
      expect(store.state.tabs[2].position! < store.state.tabs[3].position!).toBe(true)
      dispose()
    })
  })

  it('should append when the insertion anchor is missing', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1' })
      store.addTab({ type: TabType.TERMINAL, id: 't1' }, { afterKey: '1:missing' })

      expect(store.state.tabs.map(tabKey)).toEqual(['1:a1', '2:t1'])
      dispose()
    })
  })

  it('should set active tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: 'agent', id: 'a1' })
      store.addTab({ type: 'terminal', id: 't1' })
      store.setActiveTab('agent', 'a1')
      expect(store.state.activeTabKey).toBe('agent:a1')
      expect(store.activeTab()).toMatchObject({ type: 'agent', id: 'a1' })
      dispose()
    })
  })

  it('should activate most recently used tab when removing active tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: 'agent', id: 'a1' })
      store.addTab({ type: 'terminal', id: 't1' })
      store.addTab({ type: 'agent', id: 'a2' })
      // MRU: [a2, t1, a1]
      store.setActiveTab('terminal', 't1')
      // MRU: [t1, a2, a1]
      expect(store.state.activeTabKey).toBe('terminal:t1')

      // Remove t1, should activate a2 (most recently used)
      store.removeTab('terminal', 't1')
      expect(store.state.tabs.length).toBe(2)
      expect(store.state.activeTabKey).toBe('agent:a2')
      dispose()
    })
  })

  it('should activate previous tab via MRU when removing last tab in list', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: 'agent', id: 'a1' })
      store.addTab({ type: 'terminal', id: 't1' })
      // MRU: [t1, a1]; active is t1
      // Remove t1, should activate a1 (most recently used remaining)
      store.removeTab('terminal', 't1')
      expect(store.state.tabs.length).toBe(1)
      expect(store.state.activeTabKey).toBe('agent:a1')
      dispose()
    })
  })

  it('should follow MRU order across multiple closes', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: 'agent', id: 'a1' })
      store.addTab({ type: 'agent', id: 'a2' })
      store.addTab({ type: 'agent', id: 'a3' })
      store.addTab({ type: 'agent', id: 'a4' })
      // MRU: [a4, a3, a2, a1]

      // Visit tabs in order: a1 -> a3 -> a4
      store.setActiveTab('agent', 'a1')
      store.setActiveTab('agent', 'a3')
      store.setActiveTab('agent', 'a4')
      // MRU: [a4, a3, a1, a2]

      // Close a4 -> should go to a3
      store.removeTab('agent', 'a4')
      expect(store.state.activeTabKey).toBe('agent:a3')

      // Close a3 -> should go to a1 (not a2, because a1 was used more recently)
      store.removeTab('agent', 'a3')
      expect(store.state.activeTabKey).toBe('agent:a1')

      // Close a1 -> should go to a2 (last remaining)
      store.removeTab('agent', 'a1')
      expect(store.state.activeTabKey).toBe('agent:a2')

      dispose()
    })
  })

  it('should not include closed tabs in MRU when selecting next tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: 'agent', id: 'a1' })
      store.addTab({ type: 'agent', id: 'a2' })
      store.addTab({ type: 'agent', id: 'a3' })
      // MRU: [a3, a2, a1]

      store.setActiveTab('agent', 'a1')
      // MRU: [a1, a3, a2]

      // Close a1 -> should go to a3 (not a1 which is being removed)
      store.removeTab('agent', 'a1')
      expect(store.state.activeTabKey).toBe('agent:a3')

      dispose()
    })
  })

  it('should set activeTabKey to null when last tab is removed', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: 'agent', id: 'a1' })
      store.removeTab('agent', 'a1')
      expect(store.state.tabs.length).toBe(0)
      expect(store.state.activeTabKey).toBeNull()
      expect(store.activeTab()).toBeNull()
      dispose()
    })
  })

  it('should not change active tab when removing non-active tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: 'agent', id: 'a1' })
      store.addTab({ type: 'terminal', id: 't1' })
      // Active is t1
      store.removeTab('agent', 'a1')
      expect(store.state.tabs.length).toBe(1)
      expect(store.state.activeTabKey).toBe('terminal:t1')
      dispose()
    })
  })

  it('should update tab title', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: 'terminal', id: 't1' })
      store.updateTabTitle('terminal', 't1', 'bash')
      expect(store.state.tabs[0].title).toBe('bash')
      dispose()
    })
  })

  it('should clear all tabs', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: 'agent', id: 'a1' })
      store.addTab({ type: 'terminal', id: 't1' })
      store.clear()
      expect(store.state.tabs).toEqual([])
      expect(store.state.activeTabKey).toBeNull()
      dispose()
    })
  })

  it('should set notification on tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: 'terminal', id: 't1' })
      store.setNotification('terminal', 't1', true)
      expect(store.state.tabs[0].hasNotification).toBe(true)
      dispose()
    })
  })

  it('should clear notification when tab becomes active', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: 'terminal', id: 't1' })
      store.addTab({ type: 'terminal', id: 't2' })
      store.setNotification('terminal', 't1', true)
      store.setActiveTab('terminal', 't1')
      expect(store.state.tabs[0].hasNotification).toBe(false)
      dispose()
    })
  })

  it('should track per-tile active tab independently', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      const tileId = 'tile-1'

      store.addTab({ type: TabType.AGENT, id: 'a1', tileId })
      store.setActiveTabForTile(tileId, TabType.AGENT, 'a1')
      expect(store.getActiveTabKeyForTile(tileId)).toBe('1:a1')

      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId })
      store.setActiveTabForTile(tileId, TabType.TERMINAL, 't1')
      expect(store.getActiveTabKeyForTile(tileId)).toBe('2:t1')

      // Switch back to agent
      store.setActiveTabForTile(tileId, TabType.AGENT, 'a1')
      expect(store.getActiveTabKeyForTile(tileId)).toBe('1:a1')

      dispose()
    })
  })

  it('should preserve all tile tabs when switching active tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      const tileId = 'tile-1'

      store.addTab({ type: TabType.AGENT, id: 'a1', tileId })
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId })
      store.setActiveTabForTile(tileId, TabType.TERMINAL, 't1')

      // Both tabs should still exist for the tile
      const tileTabs = store.getTabsForTile(tileId)
      expect(tileTabs.length).toBe(2)
      expect(tileTabs.some(t => t.type === TabType.AGENT && t.id === 'a1')).toBe(true)
      expect(tileTabs.some(t => t.type === TabType.TERMINAL && t.id === 't1')).toBe(true)

      // Switch to agent — terminal tab must still be in the tile
      store.setActiveTabForTile(tileId, TabType.AGENT, 'a1')
      const tileTabsAfter = store.getTabsForTile(tileId)
      expect(tileTabsAfter.length).toBe(2)
      expect(tileTabsAfter.some(t => t.type === TabType.TERMINAL)).toBe(true)

      dispose()
    })
  })

  it('should detect terminal tabs for a tile after switching to agent', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      const tileId = 'tile-1'

      // Start with agent only — no terminal tabs
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId })
      store.setActiveTabForTile(tileId, TabType.AGENT, 'a1')
      expect(store.getTabsForTile(tileId).some(t => t.type === TabType.TERMINAL)).toBe(false)

      // Add a terminal and switch to it
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId })
      store.setActiveTabForTile(tileId, TabType.TERMINAL, 't1')
      expect(store.getTabsForTile(tileId).some(t => t.type === TabType.TERMINAL)).toBe(true)

      // Switch back to agent — terminal must still be detectable
      // (this is what allows TerminalView to stay mounted via CSS hiding)
      store.setActiveTabForTile(tileId, TabType.AGENT, 'a1')
      expect(store.getTabsForTile(tileId).some(t => t.type === TabType.TERMINAL)).toBe(true)

      dispose()
    })
  })

  it('should remove terminal from tile tabs when explicitly removed', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      const tileId = 'tile-1'

      store.addTab({ type: TabType.AGENT, id: 'a1', tileId })
      store.setActiveTabForTile(tileId, TabType.AGENT, 'a1')
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId })
      store.setActiveTabForTile(tileId, TabType.TERMINAL, 't1')

      // Remove the terminal tab
      store.removeTab(TabType.TERMINAL, 't1')
      expect(store.getTabsForTile(tileId).length).toBe(1)
      expect(store.getTabsForTile(tileId).some(t => t.type === TabType.TERMINAL)).toBe(false)

      // Agent should become active via MRU fallback
      expect(store.getActiveTabKeyForTile(tileId)).toBe('1:a1')

      dispose()
    })
  })

  it('should follow per-tile MRU order when closing active tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      const tileId = 'tile-1'

      store.addTab({ type: TabType.AGENT, id: 'a1', tileId })
      store.setActiveTabForTile(tileId, TabType.AGENT, 'a1')

      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId })
      store.setActiveTabForTile(tileId, TabType.TERMINAL, 't1')

      store.addTab({ type: TabType.AGENT, id: 'a2', tileId })
      store.setActiveTabForTile(tileId, TabType.AGENT, 'a2')
      // Per-tile MRU: [a2, t1, a1]

      // Close a2 → should fall back to t1 (most recent)
      store.removeTab(TabType.AGENT, 'a2')
      expect(store.getActiveTabKeyForTile(tileId)).toBe('2:t1')

      dispose()
    })
  })

  it('removeTab should update per-tile active tab when closing active tile tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      const tileId = 'tile-1'

      store.addTab({ type: TabType.AGENT, id: 'a1', tileId })
      store.setActiveTabForTile(tileId, TabType.AGENT, 'a1')

      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId })
      store.setActiveTabForTile(tileId, TabType.TERMINAL, 't1')

      store.addTab({ type: TabType.AGENT, id: 'a2', tileId })
      store.setActiveTabForTile(tileId, TabType.AGENT, 'a2')
      // Per-tile MRU: [a2, t1, a1]

      // Close a2 via removeTab → should still fall back to t1
      store.removeTab(TabType.AGENT, 'a2')
      expect(store.getActiveTabKeyForTile(tileId)).toBe('2:t1')

      dispose()
    })
  })

  it('removeTab should not change per-tile active tab when closing non-active tile tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      const tileId = 'tile-1'

      store.addTab({ type: TabType.AGENT, id: 'a1', tileId })
      store.setActiveTabForTile(tileId, TabType.AGENT, 'a1')

      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId })
      store.setActiveTabForTile(tileId, TabType.TERMINAL, 't1')
      // Per-tile MRU: [t1, a1]; active = t1

      // Close a1 (not active) via removeTab
      store.removeTab(TabType.AGENT, 'a1')
      expect(store.getActiveTabKeyForTile(tileId)).toBe('2:t1')

      dispose()
    })
  })

  it('setTabPosition should set position on a tab by key', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.setTabPosition('1:a1', 'pos-new')
      expect(store.state.tabs[0].position).toBe('pos-new')
      dispose()
    })
  })

  it('reorderTabs moves a tab to a new position and assigns a LexoRank between neighbours', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      // Two tabs in a single tile: agent first, terminal second.
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-1' })

      const initialOrder = store.state.tabs.map(t => `${t.type}:${t.id}`)
      expect(initialOrder).toEqual([`${TabType.AGENT}:a1`, `${TabType.TERMINAL}:t1`])

      // Drag terminal before agent.
      const newPosition = store.reorderTabs(`${TabType.TERMINAL}:t1`, `${TabType.AGENT}:a1`)
      expect(newPosition).not.toBeNull()

      const reorderedOrder = store.state.tabs.map(t => `${t.type}:${t.id}`)
      expect(reorderedOrder).toEqual([`${TabType.TERMINAL}:t1`, `${TabType.AGENT}:a1`])

      // The moved tab's position is updated so the new order is recoverable
      // via sortByPositions + the tab's stored position field.
      const movedTab = store.state.tabs.find(t => t.id === 't1')
      expect(movedTab?.position).toBe(newPosition)

      dispose()
    })
  })

  it('reorderTabs forward (drop after later target) places source before target with consistent position', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      // Three tabs in a single tile: [a1, a2, a3] at indices 0..2.
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-1' })
      store.addTab({ type: TabType.AGENT, id: 'a3', tileId: 'tile-1' })

      // Drag a1 forward onto a3 (fromIdx=0, toIdx=2).
      const newPosition = store.reorderTabs(`${TabType.AGENT}:a1`, `${TabType.AGENT}:a3`)
      expect(newPosition).not.toBeNull()

      // a1 should land in a3's slot, displacing a3 right: [a2, a1, a3].
      const reordered = store.state.tabs.map(t => `${t.type}:${t.id}`)
      expect(reordered).toEqual([
        `${TabType.AGENT}:a2`,
        `${TabType.AGENT}:a1`,
        `${TabType.AGENT}:a3`,
      ])

      // a1's new position must lie strictly between its new neighbours so
      // the array order matches what `sortByPositions` would produce.
      const tabs = store.state.tabs
      const movedPos = tabs[1].position
      expect(movedPos).toBe(newPosition)
      expect(movedPos! > tabs[0].position!).toBe(true)
      expect(movedPos! < tabs[2].position!).toBe(true)

      dispose()
    })
  })

  it('reorderTabs returns null when source or target is unknown', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      expect(store.reorderTabs('1:missing', '1:a1')).toBeNull()
      expect(store.reorderTabs('1:a1', '1:missing')).toBeNull()
      // Same source and target is also a no-op.
      expect(store.reorderTabs('1:a1', '1:a1')).toBeNull()
      dispose()
    })
  })

  it('snapshot+restore round-trips tab order and the active tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-1' })
      store.setActiveTabForTile('tile-1', TabType.AGENT, 'a1')
      store.reorderTabs(`${TabType.TERMINAL}:t1`, `${TabType.AGENT}:a1`)

      const snap = store.snapshot()

      // Mutate the live store to simulate a fresh page load.
      store.clear()
      expect(store.state.tabs).toEqual([])

      store.restore(snap)
      const restoredOrder = store.state.tabs.map(t => `${t.type}:${t.id}`)
      expect(restoredOrder).toEqual([`${TabType.TERMINAL}:t1`, `${TabType.AGENT}:a1`])
      // Active-tab-per-tile is preserved.
      expect(store.getActiveTabKeyForTile('tile-1')).toBe(`${TabType.AGENT}:a1`)

      dispose()
    })
  })

  it('moveTabToTile should move tab and clean up source tile MRU', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.setActiveTabForTile('tile-1', TabType.AGENT, 'a1')
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-1' })
      store.setActiveTabForTile('tile-1', TabType.TERMINAL, 't1')
      // Per-tile MRU for tile-1: [t1, a1]

      // Move terminal to tile-2
      store.moveTabToTile('2:t1', 'tile-2')

      // Terminal should now be in tile-2
      expect(store.state.tabs.find(t => t.id === 't1')?.tileId).toBe('tile-2')

      // Source tile should have fallen back to agent (MRU)
      expect(store.getActiveTabKeyForTile('tile-1')).toBe('1:a1')

      // tile-1 tabs should only have agent
      expect(store.getTabsForTile('tile-1').length).toBe(1)
      expect(store.getTabsForTile('tile-2').length).toBe(1)

      dispose()
    })
  })

  it('moveTabToTile when moved tab was active in source tile should fall back to MRU', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.setActiveTabForTile('tile-1', TabType.AGENT, 'a1')
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-1' })
      store.setActiveTabForTile('tile-1', TabType.TERMINAL, 't1')
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-1' })
      store.setActiveTabForTile('tile-1', TabType.AGENT, 'a2')
      // Per-tile MRU for tile-1: [a2, t1, a1]; active = a2

      // Move a2 (active) to tile-2
      store.moveTabToTile('1:a2', 'tile-2')

      // Source tile should fall back to t1 (next in MRU)
      expect(store.getActiveTabKeyForTile('tile-1')).toBe('2:t1')

      dispose()
    })
  })

  it('moveTabToTile when moved tab was NOT active should not change source tile active', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.setActiveTabForTile('tile-1', TabType.AGENT, 'a1')
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-1' })
      // Active in tile-1 is still a1 (t1 was added without activating)

      // Move t1 (not active) to tile-2
      store.moveTabToTile('2:t1', 'tile-2')

      // Source tile active should still be a1
      expect(store.getActiveTabKeyForTile('tile-1')).toBe('1:a1')

      dispose()
    })
  })

  it('should set display mode on a file tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.FILE, id: 'f1', filePath: '/home/user/readme.md' })
      store.setTabDisplayMode(TabType.FILE, 'f1', 'source')
      expect(store.state.tabs[0].displayMode).toBe('source')
      dispose()
    })
  })

  it('should update display mode on an existing tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.FILE, id: 'f1', filePath: '/home/user/readme.md' })
      store.setTabDisplayMode(TabType.FILE, 'f1', 'render')
      expect(store.state.tabs[0].displayMode).toBe('render')
      store.setTabDisplayMode(TabType.FILE, 'f1', 'split')
      expect(store.state.tabs[0].displayMode).toBe('split')
      dispose()
    })
  })

  it('should preserve display mode across other tab operations', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.FILE, id: 'f1', filePath: '/home/user/readme.md' })
      store.setTabDisplayMode(TabType.FILE, 'f1', 'split')
      // Add another tab — f1 display mode should persist
      store.addTab({ type: TabType.AGENT, id: 'a1' })
      expect(store.state.tabs[0].displayMode).toBe('split')
      // Update title — display mode should persist
      store.updateTabTitle(TabType.FILE, 'f1', 'renamed')
      expect(store.state.tabs[0].displayMode).toBe('split')
      dispose()
    })
  })

  it('moveTabToTile with only one tab in source tile should set active to null', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.setActiveTabForTile('tile-1', TabType.AGENT, 'a1')

      // Move the only tab to tile-2
      store.moveTabToTile('1:a1', 'tile-2')

      // Source tile should have no active tab
      expect(store.getActiveTabKeyForTile('tile-1')).toBeNull()
      expect(store.getTabsForTile('tile-1').length).toBe(0)

      dispose()
    })
  })

  it('initMissingTileActiveTabs activates first tab for tiles without active tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      // Add tabs without activation (simulates restore)
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' }, { activate: false })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-1' }, { activate: false })
      store.addTab({ type: TabType.AGENT, id: 'a3', tileId: 'tile-2' }, { activate: false })
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-3' }, { activate: false })

      // No tiles should have an active tab yet
      expect(store.getActiveTabKeyForTile('tile-1')).toBeNull()
      expect(store.getActiveTabKeyForTile('tile-2')).toBeNull()
      expect(store.getActiveTabKeyForTile('tile-3')).toBeNull()

      store.initMissingTileActiveTabs()

      // Each tile should now have its first tab active
      expect(store.getActiveTabKeyForTile('tile-1')).toBe('1:a1')
      expect(store.getActiveTabKeyForTile('tile-2')).toBe('1:a3')
      expect(store.getActiveTabKeyForTile('tile-3')).toBe('2:t1')

      dispose()
    })
  })

  it('initMissingTileActiveTabs skips tiles that already have active tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' }, { activate: false })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-1' }, { activate: false })
      store.addTab({ type: TabType.AGENT, id: 'a3', tileId: 'tile-2' }, { activate: false })

      // Manually set a2 as active in tile-1
      store.setActiveTabForTile('tile-1', TabType.AGENT, 'a2')

      store.initMissingTileActiveTabs()

      // tile-1 should still have a2 active (not overridden)
      expect(store.getActiveTabKeyForTile('tile-1')).toBe('1:a2')
      // tile-2 should now have a3 active (was missing)
      expect(store.getActiveTabKeyForTile('tile-2')).toBe('1:a3')

      dispose()
    })
  })
})

describe('terminal tab hydration fields', () => {
  it('marks exited DB-only terminals content-ready so the startup overlay cannot stick forever', () => {
    const fields = protoToTerminalTabFields('worker-1', {
      terminalId: 'term-1',
      status: TerminalStatus.READY,
      exited: true,
      screen: new Uint8Array(),
      screenEndOffset: 0n,
      title: '',
      workingDir: '/tmp',
      shellStartDir: '/tmp',
      cols: 80,
      rows: 24,
    } as any)

    expect(fields.status).toBe(TerminalStatus.EXITED)
    expect(fields.contentReady).toBe(true)
  })

  it('preserves cached title and painted-content state when rehydration has no snapshot', () => {
    const fields = preserveTerminalDisplayFields(
      {
        title: undefined,
        screen: undefined,
        lastOffset: undefined,
        contentReady: undefined,
      },
      {
        title: 'Terminal Ada',
        screen: new TextEncoder().encode('cached screen'),
        lastOffset: 42,
        contentReady: true,
      },
    )

    expect(fields.title).toBe('Terminal Ada')
    expect(fields.screen && new TextDecoder().decode(fields.screen)).toBe('cached screen')
    expect(fields.lastOffset).toBe(42)
    expect(fields.contentReady).toBe(true)
  })

  it('marks exited tabs content-ready when a close event arrives before visible output was detected', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({
        type: TabType.TERMINAL,
        id: 'term-1',
        status: TerminalStatus.READY,
        contentReady: false,
        startupMessage: 'Starting zsh…',
      })

      store.markTerminalExited('term-1')

      const tab = store.getTerminalTab('term-1')
      expect(tab?.status).toBe(TerminalStatus.EXITED)
      expect(tab?.contentReady).toBe(true)
      expect(tab?.startupMessage).toBeUndefined()
      dispose()
    })
  })
})

describe('isTabReadyForGitStatus', () => {
  // Minimal agent fixture; only the three fields the helper reads matter.
  function agent(p: Partial<Pick<AgentInfo, 'status' | 'startupMessage' | 'gitStatus'>>): AgentInfo {
    return {
      status: AgentStatus.STARTING,
      startupMessage: '',
      gitStatus: undefined,
      ...p,
    } as AgentInfo
  }

  const agentTab: Tab = { type: TabType.AGENT, id: 'a1' }

  it('treats file tabs as always ready', () => {
    const fileTab: Tab = { type: TabType.FILE, id: 'f1' }
    expect(isTabReadyForGitStatus(fileTab, null)).toBe(true)
  })

  it('treats a non-STARTING terminal tab as ready', () => {
    const ready: Tab = { type: TabType.TERMINAL, id: 't1', status: TerminalStatus.READY }
    const exited: Tab = { type: TabType.TERMINAL, id: 't1', status: TerminalStatus.EXITED }
    const failed: Tab = { type: TabType.TERMINAL, id: 't1', status: TerminalStatus.STARTUP_FAILED }
    const undef: Tab = { type: TabType.TERMINAL, id: 't1' }
    expect(isTabReadyForGitStatus(ready, null)).toBe(true)
    expect(isTabReadyForGitStatus(exited, null)).toBe(true)
    expect(isTabReadyForGitStatus(failed, null)).toBe(true)
    expect(isTabReadyForGitStatus(undef, null)).toBe(true)
  })

  it('defers a fresh STARTING terminal tab with no startupMessage', () => {
    // OpenTerminal's response leaves the tab in STARTING with no
    // phase-0 broadcast yet — same race window as agents.
    const tab: Tab = { type: TabType.TERMINAL, id: 't1', status: TerminalStatus.STARTING }
    expect(isTabReadyForGitStatus(tab, null)).toBe(false)
  })

  it('defers a STARTING terminal tab even with a phase-0 startupMessage', () => {
    // The "Creating worktree …" label is broadcast BEFORE executeGitMode
    // runs (terminal.go runTerminalPhase0), so a non-empty startupMessage
    // is not proof that the worktree is on disk. Defer until the tab
    // leaves STARTING entirely.
    const tab: Tab = {
      type: TabType.TERMINAL,
      id: 't1',
      status: TerminalStatus.STARTING,
      startupMessage: 'Creating worktree "feature"…',
    }
    expect(isTabReadyForGitStatus(tab, null)).toBe(false)
  })

  it('treats null/undefined tab as ready', () => {
    expect(isTabReadyForGitStatus(null, null)).toBe(true)
    expect(isTabReadyForGitStatus(undefined, null)).toBe(true)
  })

  it('treats an agent tab with no matching agent as ready', () => {
    expect(isTabReadyForGitStatus(agentTab, null)).toBe(true)
    expect(isTabReadyForGitStatus(agentTab, undefined)).toBe(true)
  })

  it('defers in the initial STARTING state — no startupMessage and no gitStatus', () => {
    // The window between OpenAgent's response and any broadcast.
    expect(
      isTabReadyForGitStatus(
        agentTab,
        agent({ status: AgentStatus.STARTING, startupMessage: '', gitStatus: undefined }),
      ),
    ).toBe(false)
  })

  it('defers a STARTING agent with a phase-0 startupMessage', () => {
    // Phase 0 broadcasts "Creating worktree …" BEFORE executeGitMode
    // runs, so a non-empty startupMessage means nothing about disk state.
    expect(
      isTabReadyForGitStatus(
        agentTab,
        agent({ status: AgentStatus.STARTING, startupMessage: 'Creating worktree "feature"…' }),
      ),
    ).toBe(false)
  })

  it('defers a STARTING agent in the phase-1 window', () => {
    expect(
      isTabReadyForGitStatus(
        agentTab,
        agent({ status: AgentStatus.STARTING, startupMessage: 'Checking Git status…' }),
      ),
    ).toBe(false)
  })

  it('defers a STARTING agent in the phase-2 window even with gitStatus set', () => {
    // gitStatus arrives at the start of phase 2, before the worktree is
    // reliably observable to a separate process — see the helper docstring.
    expect(
      isTabReadyForGitStatus(
        agentTab,
        agent({
          status: AgentStatus.STARTING,
          startupMessage: 'Starting Claude Code…',
          gitStatus: { branch: 'main' } as AgentInfo['gitStatus'],
        }),
      ),
    ).toBe(false)
  })

  it('is ready in any non-STARTING state regardless of message/gitStatus', () => {
    for (const status of [AgentStatus.ACTIVE, AgentStatus.INACTIVE, AgentStatus.STARTUP_FAILED]) {
      expect(
        isTabReadyForGitStatus(
          agentTab,
          agent({ status, startupMessage: '', gitStatus: undefined }),
        ),
      ).toBe(true)
    }
  })
})

describe('reassignTabsToTile', () => {
  it('moves matching tabs onto the new tile id', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 't-a' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 't-b' })
      store.addTab({ type: TabType.AGENT, id: 'a3', tileId: 't-other' })

      store.reassignTabsToTile(['t-a', 't-b'], 't-merged')

      const byId = Object.fromEntries(store.state.tabs.map(t => [t.id, t.tileId]))
      expect(byId.a1).toBe('t-merged')
      expect(byId.a2).toBe('t-merged')
      expect(byId.a3).toBe('t-other')
      dispose()
    })
  })

  it('uses the active key from the first source tile that has one', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      // t-a has no active key (empty), t-b has one — t-b's active key wins.
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 't-b' })
      // Drop t-a's MRU/active by giving it a tab and removing it.
      store.addTab({ type: TabType.AGENT, id: 'a-temp', tileId: 't-a' })
      store.removeTab(TabType.AGENT, 'a-temp')

      store.reassignTabsToTile(['t-a', 't-b'], 't-merged')
      expect(store.state.tileActiveTabKeys['t-merged']).toBe('1:a1')
      dispose()
    })
    createRoot((dispose) => {
      const store = createTabStore()
      // Both tiles have active keys → first source tile wins (t-a).
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 't-a' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 't-b' })
      store.reassignTabsToTile(['t-a', 't-b'], 't-merged')
      expect(store.state.tileActiveTabKeys['t-merged']).toBe('1:a1')
      dispose()
    })
  })

  it('falls back to the merged MRU first entry if no source tile has an active key', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      // Add and remove tabs to leave MRU populated but no active.
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 't-a' })
      store.removeTab(TabType.AGENT, 'a1')
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 't-a' })
      store.removeTab(TabType.AGENT, 'a2')
      // Both tiles have empty MRU/active now; reassignment yields no active.
      store.reassignTabsToTile(['t-a', 't-b'], 't-merged')
      expect(store.state.tileActiveTabKeys['t-merged']).toBeNull()
      dispose()
    })
  })

  it('merges MRU lists in oldTileIds order, dedupe-by-first', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      // Tile A's MRU: a1, a2 — after addTab calls.
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 't-a' }) // a1 active in t-a
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 't-a' }) // a2 active, MRU=[a2,a1]
      // Tile B's MRU: a3, a2-duplicate-key (different tab type to keep distinct).
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 't-b' })
      // Reassign t-a, t-b → t-merged in that order.
      store.reassignTabsToTile(['t-a', 't-b'], 't-merged')
      const merged = store.state.tileMruOrder['t-merged']
      // First t-a's MRU (most-recent first), then t-b's.
      expect(merged).toEqual(['1:a2', '1:a1', '2:t1'])
      dispose()
    })
  })

  it('cleans up stale per-tile state for source tiles', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 't-a' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 't-b' })
      store.reassignTabsToTile(['t-a', 't-b'], 't-merged')
      expect(store.state.tileActiveTabKeys['t-a']).toBeUndefined()
      expect(store.state.tileMruOrder['t-a']).toBeUndefined()
      expect(store.state.tileActiveTabKeys['t-b']).toBeUndefined()
      expect(store.state.tileMruOrder['t-b']).toBeUndefined()
      dispose()
    })
  })
})

describe('cleanupTiles', () => {
  it('drops MRU and active-tab entries for every passed tile id', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 't-a' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 't-b' })
      store.addTab({ type: TabType.AGENT, id: 'a3', tileId: 't-keep' })
      // Remove the actual tabs first so the per-tile records are the only
      // remaining bookkeeping.
      store.removeTab(TabType.AGENT, 'a1')
      store.removeTab(TabType.AGENT, 'a2')
      // The keep tile's records should survive.
      const beforeKeepActive = store.state.tileActiveTabKeys['t-keep']
      const beforeKeepMru = store.state.tileMruOrder['t-keep']

      store.cleanupTiles(['t-a', 't-b'])

      expect(store.state.tileActiveTabKeys['t-a']).toBeUndefined()
      expect(store.state.tileMruOrder['t-a']).toBeUndefined()
      expect(store.state.tileActiveTabKeys['t-b']).toBeUndefined()
      expect(store.state.tileMruOrder['t-b']).toBeUndefined()
      expect(store.state.tileActiveTabKeys['t-keep']).toBe(beforeKeepActive)
      expect(store.state.tileMruOrder['t-keep']).toBe(beforeKeepMru)
      dispose()
    })
  })

  it('is a no-op for an empty iterable', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 't-a' })
      const beforeActive = store.state.tileActiveTabKeys
      const beforeMru = store.state.tileMruOrder

      store.cleanupTiles([])

      // Same map references — nothing was rewritten.
      expect(store.state.tileActiveTabKeys).toBe(beforeActive)
      expect(store.state.tileMruOrder).toBe(beforeMru)
      dispose()
    })
  })

  it('cleanupTile delegates to cleanupTiles (single-id parity)', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 't-a' })
      store.removeTab(TabType.AGENT, 'a1')
      expect(store.state.tileActiveTabKeys['t-a']).toBeDefined()

      store.cleanupTile('t-a')

      expect(store.state.tileActiveTabKeys['t-a']).toBeUndefined()
      expect(store.state.tileMruOrder['t-a']).toBeUndefined()
      dispose()
    })
  })
})

describe('mergeTabsIntoTile', () => {
  it('moves source tabs onto the target and preserves the target active tab', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'tgt-a', tileId: 't-target' }) // active in target
      store.addTab({ type: TabType.AGENT, id: 'src-a', tileId: 't-source' }) // active in source

      store.mergeTabsIntoTile('t-source', 't-target')

      // Source tab's tileId is rewritten.
      const src = store.state.tabs.find(t => t.id === 'src-a')
      expect(src?.tileId).toBe('t-target')
      // Target's existing active tab key is preserved.
      expect(store.state.tileActiveTabKeys['t-target']).toBe('1:tgt-a')
      // Source's per-tile state is cleared.
      expect(store.state.tileActiveTabKeys['t-source']).toBeUndefined()
      expect(store.state.tileMruOrder['t-source']).toBeUndefined()
      dispose()
    })
  })

  it('positions source tabs after the target last tab via lexorank', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'tgt-a', tileId: 't-target' })
      store.addTab({ type: TabType.AGENT, id: 'tgt-b', tileId: 't-target' })
      store.addTab({ type: TabType.AGENT, id: 'src-a', tileId: 't-source' })
      store.addTab({ type: TabType.AGENT, id: 'src-b', tileId: 't-source' })

      store.mergeTabsIntoTile('t-source', 't-target')

      const onTarget = store.getTabsForTile('t-target')
      // Sort by position; expect target tabs first, then source tabs in
      // their original order.
      const sorted = [...onTarget].sort((a, b) => (a.position ?? '').localeCompare(b.position ?? ''))
      expect(sorted.map(t => t.id)).toEqual(['tgt-a', 'tgt-b', 'src-a', 'src-b'])
      dispose()
    })
  })

  it('appends source MRU after target MRU and dedupes', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      // Target MRU = [tgt-b, tgt-a] (after addTab calls; latest first).
      store.addTab({ type: TabType.AGENT, id: 'tgt-a', tileId: 't-target' })
      store.addTab({ type: TabType.AGENT, id: 'tgt-b', tileId: 't-target' })
      // Source MRU = [src-b, src-a].
      store.addTab({ type: TabType.AGENT, id: 'src-a', tileId: 't-source' })
      store.addTab({ type: TabType.AGENT, id: 'src-b', tileId: 't-source' })

      store.mergeTabsIntoTile('t-source', 't-target')

      expect(store.state.tileMruOrder['t-target']).toEqual(['1:tgt-b', '1:tgt-a', '1:src-b', '1:src-a'])
      dispose()
    })
  })

  it('adopts the source active when the target had none', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      // Target with no active tab: addTab sets one, then we clear via remove.
      store.addTab({ type: TabType.AGENT, id: 'tgt-tmp', tileId: 't-target' })
      store.removeTab(TabType.AGENT, 'tgt-tmp')
      expect(store.state.tileActiveTabKeys['t-target']).toBeNull()

      store.addTab({ type: TabType.AGENT, id: 'src-a', tileId: 't-source' })
      store.mergeTabsIntoTile('t-source', 't-target')

      expect(store.state.tileActiveTabKeys['t-target']).toBe('1:src-a')
      dispose()
    })
  })

  it('is a no-op when source and target are the same tile', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 't-x' })
      const before = store.state.tabs[0].position
      store.mergeTabsIntoTile('t-x', 't-x')
      expect(store.state.tabs[0].tileId).toBe('t-x')
      expect(store.state.tabs[0].position).toBe(before)
      expect(store.state.tileActiveTabKeys['t-x']).toBe('1:a1')
      dispose()
    })
  })

  it('still cleans up source state when source has no tabs', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      // Stale source state with no tabs: a tab was added then removed.
      store.addTab({ type: TabType.AGENT, id: 'tmp', tileId: 't-source' })
      store.removeTab(TabType.AGENT, 'tmp')
      // Stamp a fake active to confirm cleanup runs.
      // (After remove the active is null; mergeTabsIntoTile must drop the
      // entry entirely so future reads on the recycled tile id don't see it.)
      store.addTab({ type: TabType.AGENT, id: 'tgt-a', tileId: 't-target' })

      store.mergeTabsIntoTile('t-source', 't-target')

      expect(store.state.tileActiveTabKeys['t-source']).toBeUndefined()
      expect(store.state.tileMruOrder['t-source']).toBeUndefined()
      // Target untouched.
      expect(store.state.tileActiveTabKeys['t-target']).toBe('1:tgt-a')
      dispose()
    })
  })
})

// `getTabsForTile` is read 3-4× per tile per render and is now backed by
// a `tabsByTile` memo. These tests exercise every membership-changing
// mutation path and confirm the memo returns the right tabs after each.
describe('getTabsForTile (per-tile index)', () => {
  function tabIds(tabs: Tab[]): string[] {
    return tabs.map(t => t.id).sort()
  }

  it('returns an empty array for an unknown tile', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      expect(store.getTabsForTile('missing')).toEqual([])
      dispose()
    })
  })

  it('skips tabs without a tileId', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      // Tab created without a tileId — must not show up under any tile.
      store.addTab({ type: TabType.AGENT, id: 'a1' })
      expect(store.getTabsForTile('tile-1')).toEqual([])
      dispose()
    })
  })

  it('addTab with a tileId places the tab under that tile', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-1' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-2' })
      expect(tabIds(store.getTabsForTile('tile-1'))).toEqual(['a1', 't1'])
      expect(tabIds(store.getTabsForTile('tile-2'))).toEqual(['a2'])
      dispose()
    })
  })

  it('removeTab drops the tab from its tile bucket', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-1' })
      store.removeTab(TabType.AGENT, 'a1')
      expect(tabIds(store.getTabsForTile('tile-1'))).toEqual(['t1'])
      dispose()
    })
  })

  it('moveTabToTile moves the tab between buckets', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.moveTabToTile('1:a1', 'tile-2')
      expect(tabIds(store.getTabsForTile('tile-1'))).toEqual([])
      expect(tabIds(store.getTabsForTile('tile-2'))).toEqual(['a1'])
      dispose()
    })
  })

  it('reassignTabsToTile bulk-moves every tab from old tiles to the new one', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-a' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-b' })
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-c' })
      store.reassignTabsToTile(['tile-a', 'tile-b'], 'tile-merged')
      expect(tabIds(store.getTabsForTile('tile-a'))).toEqual([])
      expect(tabIds(store.getTabsForTile('tile-b'))).toEqual([])
      expect(tabIds(store.getTabsForTile('tile-merged'))).toEqual(['a1', 'a2'])
      // Untouched tile keeps its tab.
      expect(tabIds(store.getTabsForTile('tile-c'))).toEqual(['t1'])
      dispose()
    })
  })

  it('updateTab can change tileId and the index follows', () => {
    // updateTab is a generic field-setter that some callers use to retag
    // a tab onto a different tile. The index must reflect that.
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.updateTab(TabType.AGENT, 'a1', { tileId: 'tile-2' })
      expect(tabIds(store.getTabsForTile('tile-1'))).toEqual([])
      expect(tabIds(store.getTabsForTile('tile-2'))).toEqual(['a1'])
      dispose()
    })
  })

  it('clear empties every tile bucket', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-2' })
      store.clear()
      expect(store.getTabsForTile('tile-1')).toEqual([])
      expect(store.getTabsForTile('tile-2')).toEqual([])
      dispose()
    })
  })

  it('snapshot + restore round-trips the index', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-1' })
      const snap = store.snapshot()

      // Mutate, then restore.
      store.removeTab(TabType.AGENT, 'a1')
      expect(tabIds(store.getTabsForTile('tile-1'))).toEqual(['t1'])
      store.restore(snap)
      expect(tabIds(store.getTabsForTile('tile-1'))).toEqual(['a1', 't1'])
      dispose()
    })
  })

  it('reorderTabs preserves tile membership (only positions change)', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-1' })
      store.addTab({ type: TabType.AGENT, id: 'a3', tileId: 'tile-2' })
      store.reorderTabs('1:a1', '1:a2')
      expect(tabIds(store.getTabsForTile('tile-1'))).toEqual(['a1', 'a2'])
      expect(tabIds(store.getTabsForTile('tile-2'))).toEqual(['a3'])
      dispose()
    })
  })

  it('per-field mutations (title, notification) keep the tab in the index', () => {
    // updateTabTitle / setNotification target one field via path-update.
    // The cached array still holds the live store proxy; reads through it
    // see the new title without the index needing to be rebuilt.
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1', title: 'Old' })
      store.updateTabTitle(TabType.AGENT, 'a1', 'New')
      const tabs = store.getTabsForTile('tile-1')
      expect(tabs).toHaveLength(1)
      expect(tabs[0].title).toBe('New')
      store.setNotification(TabType.AGENT, 'a1', true)
      expect(store.getTabsForTile('tile-1')[0].hasNotification).toBe(true)
      dispose()
    })
  })

  it('multi-tile mix: each tile only sees its own tabs', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-1' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-2' })
      store.addTab({ type: TabType.FILE, id: 'f1', tileId: 'tile-3' })
      store.addTab({ type: TabType.AGENT, id: 'a3' }) // no tileId
      expect(tabIds(store.getTabsForTile('tile-1'))).toEqual(['a1', 't1'])
      expect(tabIds(store.getTabsForTile('tile-2'))).toEqual(['a2'])
      expect(tabIds(store.getTabsForTile('tile-3'))).toEqual(['f1'])
      dispose()
    })
  })
})
