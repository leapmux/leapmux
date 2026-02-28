import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createTabStore, tabKey, TabType } from '~/stores/tab.store'

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
      store.removeTabFromTile(TabType.TERMINAL, 't1', tileId)
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
      store.removeTabFromTile(TabType.AGENT, 'a2', tileId)
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

      // Close a2 via removeTab (not removeTabFromTile) → should still fall back to t1
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
})
