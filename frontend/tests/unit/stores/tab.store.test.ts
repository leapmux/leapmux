import type { AgentInfo } from '~/generated/leapmux/v1/agent_pb'
import type { Tab } from '~/stores/tab.types'
import { createEffect, createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentStatus } from '~/generated/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { isTabReadyForGitStatus, preserveTerminalDisplayFields, protoToTerminalTabFields, tabKey } from '~/stores/tab.helpers'
import { createTabStore } from '~/stores/tab.store'
import { flush } from '../helpers/async'

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

  // Regression: HMR reload + concurrent worker-restore-vs-reconciler
  // races used to produce two `state.tabs` entries with the same key
  // — one populated by `useWorkspaceRestore` (with title /
  // agentProvider), one inserted bare by the projection reconciler's
  // step 2 (just CRDT-driven fields). Both rendered in the sidebar
  // / tabbar; clicking close on either dropped both because
  // `removeTab` filters by key. `addTab` now treats a duplicate key
  // as a no-op so the authoritative first insert (typically the one
  // with worker-supplied metadata) wins.
  it('dedupes addTab by (type, id) so duplicate calls do not produce two rows', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({
        type: TabType.AGENT,
        id: 'agent-x',
        title: 'Agent Sullivan',
        agentProvider: 1,
        workerId: 'w-1',
        tileId: 'tile-1',
      })
      // Reconciler-style bare re-add for the same id. Must not append
      // a second row with empty title.
      store.addTab({
        type: TabType.AGENT,
        id: 'agent-x',
        tileId: 'tile-1',
      }, { activate: false, silent: true })

      expect(store.state.tabs).toHaveLength(1)
      const only = store.state.tabs[0]
      expect(only.title).toBe('Agent Sullivan')
      expect(only.agentProvider).toBe(1)
      expect(only.workerId).toBe('w-1')
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

  it('reorderTabs forward (drop onto later target) places source after target with consistent position', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      // Three tabs in a single tile: [a1, a2, a3] at indices 0..2.
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-1' })
      store.addTab({ type: TabType.AGENT, id: 'a3', tileId: 'tile-1' })

      // Drag a1 forward onto a3 (fromIdx=0, toIdx=2).
      const newPosition = store.reorderTabs(`${TabType.AGENT}:a1`, `${TabType.AGENT}:a3`)
      expect(newPosition).not.toBeNull()

      // Swap-on-cross: a1 crosses a3 and lands after it: [a2, a3, a1].
      const reordered = store.state.tabs.map(t => `${t.type}:${t.id}`)
      expect(reordered).toEqual([
        `${TabType.AGENT}:a2`,
        `${TabType.AGENT}:a3`,
        `${TabType.AGENT}:a1`,
      ])

      // a1's new position must sort strictly after its left neighbour so
      // the array order matches what `sortByPositions` would produce.
      const tabs = store.state.tabs
      const movedPos = tabs[2].position
      expect(movedPos).toBe(newPosition)
      expect(movedPos! > tabs[1].position!).toBe(true)

      dispose()
    })
  })

  it('reorderTabs forward onto immediate right neighbour swaps the two tabs', () => {
    // Regression: dragging A onto its immediate right neighbour B in [A, B, C]
    // previously did nothing because the insert-before semantic reinserted A
    // at its original slot.
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-1' })
      store.addTab({ type: TabType.AGENT, id: 'a3', tileId: 'tile-1' })

      const newPosition = store.reorderTabs(`${TabType.AGENT}:a1`, `${TabType.AGENT}:a2`)
      expect(newPosition).not.toBeNull()

      const reordered = store.state.tabs.map(t => `${t.type}:${t.id}`)
      expect(reordered).toEqual([
        `${TabType.AGENT}:a2`,
        `${TabType.AGENT}:a1`,
        `${TabType.AGENT}:a3`,
      ])

      const tabs = store.state.tabs
      const movedPos = tabs[1].position
      expect(movedPos).toBe(newPosition)
      expect(movedPos! > tabs[0].position!).toBe(true)
      expect(movedPos! < tabs[2].position!).toBe(true)

      dispose()
    })
  })

  it('reorderTabs backward onto immediate left neighbour swaps the two tabs', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-1' })
      store.addTab({ type: TabType.AGENT, id: 'a3', tileId: 'tile-1' })

      // Drag a3 backward onto a2 (fromIdx=2, toIdx=1).
      const newPosition = store.reorderTabs(`${TabType.AGENT}:a3`, `${TabType.AGENT}:a2`)
      expect(newPosition).not.toBeNull()

      const reordered = store.state.tabs.map(t => `${t.type}:${t.id}`)
      expect(reordered).toEqual([
        `${TabType.AGENT}:a1`,
        `${TabType.AGENT}:a3`,
        `${TabType.AGENT}:a2`,
      ])

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

describe('stampBranchOnTabs', () => {
  it('writes gitBranch onto every tab matching (workerId, workingDir) and returns true', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({
        type: TabType.AGENT,
        id: 'a1',
        tileId: 'tile-1',
        workerId: 'w1',
        gitToplevel: '/repo',
        gitBranch: 'old',
      })
      store.addTab({
        type: TabType.TERMINAL,
        id: 't1',
        tileId: 'tile-1',
        workerId: 'w1',
        gitToplevel: '/repo',
        gitBranch: 'old',
      })

      const wrote = store.stampBranchOnTabs('w1', '/repo', 'new')

      expect(wrote).toBe(true)
      expect(store.state.tabs.find(t => t.id === 'a1')?.gitBranch).toBe('new')
      expect(store.state.tabs.find(t => t.id === 't1')?.gitBranch).toBe('new')
      dispose()
    })
  })

  it('returns false and does not write when every matching tab already has the new branch', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({
        type: TabType.AGENT,
        id: 'a1',
        tileId: 'tile-1',
        workerId: 'w1',
        gitToplevel: '/repo',
        gitBranch: 'main',
      })
      // Snapshot the array reference; with no write, setState should not
      // produce a fresh array. Solid's path-based setState replaces the
      // mutated index even when fields are identical — the guard is what
      // keeps this no-op cheap downstream.
      const before = store.state.tabs[0]

      const wrote = store.stampBranchOnTabs('w1', '/repo', 'main')

      expect(wrote).toBe(false)
      // Identity preserved — the row was not re-projected.
      expect(store.state.tabs[0]).toBe(before)
      dispose()
    })
  })

  it('returns true and writes when at least one matching tab is stale, even if others already match', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({
        type: TabType.AGENT,
        id: 'a1',
        tileId: 'tile-1',
        workerId: 'w1',
        gitToplevel: '/repo',
        gitBranch: 'new', // already correct
      })
      store.addTab({
        type: TabType.TERMINAL,
        id: 't1',
        tileId: 'tile-1',
        workerId: 'w1',
        gitToplevel: '/repo',
        gitBranch: 'old', // stale
      })

      const wrote = store.stampBranchOnTabs('w1', '/repo', 'new')

      expect(wrote).toBe(true)
      expect(store.state.tabs.find(t => t.id === 't1')?.gitBranch).toBe('new')
      // The behavior we care about: a single stale entry forces a write
      // that covers every matching row uniformly.
      dispose()
    })
  })

  it('only rewrites stale rows; already-correct siblings stay untouched at the field level', async () => {
    // The setState predicate now folds the staleness check in — only
    // rows whose gitBranch actually differs get rewritten. Without
    // this, every matching row's gitBranch field would be set to the
    // same value via Solid's setState path, and even though Solid
    // dedupes by ===, the updater itself runs once per matched row.
    //
    // Observable contract: per-tab effects that subscribe to
    // `gitBranch` refire only for the stale row, not the
    // already-correct one. Pin the count so a regression to the
    // broader matches() predicate would refire both.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const store = createTabStore()
        store.addTab({
          type: TabType.AGENT,
          id: 'a1',
          tileId: 'tile-1',
          workerId: 'w1',
          gitToplevel: '/repo',
          gitBranch: 'new', // already correct
        })
        store.addTab({
          type: TabType.TERMINAL,
          id: 't1',
          tileId: 'tile-1',
          workerId: 'w1',
          gitToplevel: '/repo',
          gitBranch: 'old', // stale
        })

        const correctTab = store.state.tabs.find(t => t.id === 'a1')!
        const staleTab = store.state.tabs.find(t => t.id === 't1')!
        let correctRuns = 0
        let staleRuns = 0
        createEffect(() => {
          void correctTab.gitBranch
          correctRuns++
        })
        createEffect(() => {
          void staleTab.gitBranch
          staleRuns++
        })
        await flush()
        expect(correctRuns).toBe(1)
        expect(staleRuns).toBe(1)

        const wrote = store.stampBranchOnTabs('w1', '/repo', 'new')
        await flush()
        expect(wrote).toBe(true)
        // Already-correct row's gitBranch was NOT touched by setState,
        // so its subscribers don't refire.
        expect(correctRuns).toBe(1)
        // Stale row's gitBranch advances from 'old' → 'new', firing its
        // subscriber exactly once.
        expect(staleRuns).toBe(2)
        expect(store.state.tabs.find(t => t.id === 't1')?.gitBranch).toBe('new')
        dispose()
        done()
      })
    })
  })

  it('does not touch tabs in a different worker or different working dir', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({
        type: TabType.AGENT,
        id: 'target',
        tileId: 'tile-1',
        workerId: 'w1',
        gitToplevel: '/repo',
        gitBranch: 'old',
      })
      store.addTab({
        type: TabType.AGENT,
        id: 'other-worker',
        tileId: 'tile-1',
        workerId: 'w2',
        gitToplevel: '/repo',
        gitBranch: 'old',
      })
      store.addTab({
        type: TabType.AGENT,
        id: 'other-dir',
        tileId: 'tile-1',
        workerId: 'w1',
        gitToplevel: '/elsewhere',
        gitBranch: 'old',
      })

      store.stampBranchOnTabs('w1', '/repo', 'new')

      expect(store.state.tabs.find(t => t.id === 'target')?.gitBranch).toBe('new')
      expect(store.state.tabs.find(t => t.id === 'other-worker')?.gitBranch).toBe('old')
      expect(store.state.tabs.find(t => t.id === 'other-dir')?.gitBranch).toBe('old')
      dispose()
    })
  })

  it('returns false when workingDir is empty (no real repo identity → no stamp)', () => {
    // Regression: an earlier revision treated `workingDir = ''` as a
    // wildcard via `(t.gitToplevel ?? '') === ''`, so a stamp call on
    // an unstamped repo bled the new branch name onto every other
    // unstamped repo's tabs on the same worker. The empty-workingDir
    // path is now a no-op; callers must resolve a real repo path first.
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({
        type: TabType.AGENT,
        id: 'a1',
        tileId: 'tile-1',
        workerId: 'w1',
        // gitToplevel intentionally omitted — pre-fix, the empty
        // workingDir argument would have matched this.
        gitBranch: 'old',
      })

      const wrote = store.stampBranchOnTabs('w1', '', 'new')

      expect(wrote).toBe(false)
      expect(store.state.tabs[0].gitBranch).toBe('old')
      dispose()
    })
  })

  it('returns false when workerId is empty (no real worker identity → no stamp)', () => {
    // Symmetric companion to the workingDir='' guard: BranchGroup.workerId
    // defaults to '' for tabs whose workerId hasn't landed yet (buildTree
    // resolves it via `tab.workerId ?? ''`), so a Change/Delete branch
    // dispatched from that row before its worker id lands would otherwise
    // cross-stamp every other unworker-bound tab whose gitToplevel
    // matches. The empty-workerId path must be a no-op.
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({
        type: TabType.AGENT,
        id: 'a1',
        tileId: 'tile-1',
        // workerId intentionally omitted to mirror the buildTree default.
        gitToplevel: '/repo',
        gitBranch: 'old',
      })

      const wrote = store.stampBranchOnTabs('', '/repo', 'new')

      expect(wrote).toBe(false)
      expect(store.state.tabs[0].gitBranch).toBe('old')
      dispose()
    })
  })

  it('returns false when no tab matches the predicate at all', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({
        type: TabType.AGENT,
        id: 'a1',
        tileId: 'tile-1',
        workerId: 'w1',
        gitToplevel: '/repo',
        gitBranch: 'main',
      })

      const wrote = store.stampBranchOnTabs('w2', '/elsewhere', 'feature')

      expect(wrote).toBe(false)
      expect(store.state.tabs[0].gitBranch).toBe('main')
      dispose()
    })
  })
})

describe('updateMatchingTabs', () => {
  it('spreads fields onto every tab matching the predicate in one mutation', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1', workerId: 'w1', gitBranch: 'old' })
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'tile-1', workerId: 'w1', gitBranch: 'old' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-2', workerId: 'w2', gitBranch: 'old' })

      store.updateMatchingTabs(t => t.workerId === 'w1', { gitBranch: 'new' })

      expect(store.state.tabs.find(t => t.id === 'a1')?.gitBranch).toBe('new')
      expect(store.state.tabs.find(t => t.id === 't1')?.gitBranch).toBe('new')
      // Non-matching worker is untouched.
      expect(store.state.tabs.find(t => t.id === 'a2')?.gitBranch).toBe('old')
      dispose()
    })
  })

  it('equalsFields short-circuits rows already carrying the target fields', () => {
    // Without the guard, Solid's path-form setState would replace the
    // matching row's proxy identity even when no field actually
    // differs — re-firing every dependent memo (sidebar, tabbar,
    // tooltip). With equalsFields the no-op rows keep their proxy
    // identity and the matching-but-stale row is the only one written.
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'stale', tileId: 'tile-1', workerId: 'w1', gitBranch: 'old' })
      store.addTab({ type: TabType.TERMINAL, id: 'fresh', tileId: 'tile-1', workerId: 'w1', gitBranch: 'new' })

      const freshBefore = store.state.tabs.find(t => t.id === 'fresh')

      store.updateMatchingTabs(
        t => t.workerId === 'w1',
        { gitBranch: 'new' },
        t => t.gitBranch === 'new',
      )

      // Stale row was projected.
      expect(store.state.tabs.find(t => t.id === 'stale')?.gitBranch).toBe('new')
      // Already-matching row kept its proxy identity (no re-spread).
      expect(store.state.tabs.find(t => t.id === 'fresh')).toBe(freshBefore)
      dispose()
    })
  })

  it('equalsFields=true on every row results in zero writes', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1', workerId: 'w1', gitBranch: 'main' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-1', workerId: 'w1', gitBranch: 'main' })

      const refs = store.state.tabs.map(t => t)

      store.updateMatchingTabs(
        t => t.workerId === 'w1',
        { gitBranch: 'main' },
        () => true,
      )

      // No proxy identity changed — every tab is the same row object.
      expect(store.state.tabs[0]).toBe(refs[0])
      expect(store.state.tabs[1]).toBe(refs[1])
      dispose()
    })
  })
})

describe('tab.store setter no-op guards', () => {
  // The setters short-circuit when the incoming value matches the
  // existing value via a `&& t.field !== value` clause in the path
  // predicate. Without the guard, Solid's path-form setState replaces
  // the row identity even when the assignment is a no-op, firing every
  // reactive consumer of that field — measurable here as createEffect
  // re-runs.
  it('updateTabTitle: identical-title write does not re-fire title subscribers', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1', title: 'Hello' })

        let titleEffectRuns = 0
        createEffect(() => {
          // Subscribe specifically to the title field of tab a1.
          void store.state.tabs.find(t => t.id === 'a1')?.title
          titleEffectRuns++
        })
        await flush()
        // Initial run from the effect's first execution.
        expect(titleEffectRuns).toBe(1)

        store.updateTabTitle(TabType.AGENT, 'a1', 'Hello')
        await flush()
        // Same-value write must not retrigger the title subscriber.
        expect(titleEffectRuns).toBe(1)

        store.updateTabTitle(TabType.AGENT, 'a1', 'Different')
        await flush()
        // Real change retriggers.
        expect(titleEffectRuns).toBe(2)
        dispose()
        done()
      })
    })
  })

  it('setNotification: identical-flag write does not re-fire hasNotification subscribers', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.TERMINAL, id: 't1' })

        let runs = 0
        createEffect(() => {
          void store.state.tabs.find(t => t.id === 't1')?.hasNotification
          runs++
        })
        await flush()
        expect(runs).toBe(1)

        // Tab starts with hasNotification undefined; the guard normalizes
        // to `!!t.hasNotification !== hasNotification`, so setting to false
        // on a fresh tab is a no-op.
        store.setNotification(TabType.TERMINAL, 't1', false)
        await flush()
        expect(runs).toBe(1)

        store.setNotification(TabType.TERMINAL, 't1', true)
        await flush()
        expect(runs).toBe(2)

        // Re-setting to the same true value is a no-op.
        store.setNotification(TabType.TERMINAL, 't1', true)
        await flush()
        expect(runs).toBe(2)
        dispose()
        done()
      })
    })
  })

  it('setTabPosition: identical-position write does not re-fire position subscribers', async () => {
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1' })
        store.setTabPosition('1:a1', 'pos-1')

        let runs = 0
        createEffect(() => {
          void store.state.tabs.find(t => t.id === 'a1')?.position
          runs++
        })
        await flush()
        expect(runs).toBe(1)

        store.setTabPosition('1:a1', 'pos-1')
        await flush()
        expect(runs).toBe(1)

        store.setTabPosition('1:a1', 'pos-2')
        await flush()
        expect(runs).toBe(2)
        dispose()
        done()
      })
    })
  })

  it('updateTab: identical-fields write does not re-fire field subscribers', async () => {
    // updateTab is the multi-field cousin of updateTabTitle / setNotification.
    // The same path-form setState semantics apply: assigning identical
    // values still replaces the row proxy and re-fires every dependent
    // memo unless the predicate short-circuits. The guard checks each
    // supplied field against `prev` and skips the spread when nothing
    // changed.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const store = createTabStore()
        store.addTab({
          type: TabType.AGENT,
          id: 'a1',
          title: 'Hello',
          gitBranch: 'main',
        })

        // Track field reads directly: Solid's store proxy registers a
        // dependency on each field when read, so a setState path-form
        // assignment to that field fires this effect. Reading via the
        // tabs[] array (find()) wouldn't track field-level changes.
        let runs = 0
        createEffect(() => {
          const tab = store.state.tabs.find(t => t.id === 'a1')
          // Touch both fields the test will assert on, so a path-form
          // write to either re-fires this subscriber.
          void tab?.title
          void tab?.gitBranch
          runs++
        })
        await flush()
        expect(runs).toBe(1)

        // All supplied fields match prev — must be a complete no-op.
        store.updateTab(TabType.AGENT, 'a1', { title: 'Hello', gitBranch: 'main' })
        await flush()
        expect(runs).toBe(1)

        // Partial overlap: one matching field, one new. The mismatch
        // forces the write so subscribers re-run.
        store.updateTab(TabType.AGENT, 'a1', { title: 'Hello', gitBranch: 'feature' })
        await flush()
        expect(runs).toBe(2)
        expect(store.state.tabs.find(t => t.id === 'a1')?.gitBranch).toBe('feature')

        // Re-writing the same payload after a real change is also a no-op.
        store.updateTab(TabType.AGENT, 'a1', { title: 'Hello', gitBranch: 'feature' })
        await flush()
        expect(runs).toBe(2)
        dispose()
        done()
      })
    })
  })

  it('updateTab: empty fields object is a no-op', async () => {
    // Defensive: callers that conditionally build the fields object can
    // end up passing `{}`. The predicate must treat "no keys" as "no
    // change" rather than replacing the row with a spread of nothing.
    // `Object.keys({}).some(...)` is `false`, so the predicate never
    // matches and setState skips the row entirely.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1', title: 'Hello' })

        let runs = 0
        createEffect(() => {
          void store.state.tabs.find(t => t.id === 'a1')?.title
          runs++
        })
        await flush()
        expect(runs).toBe(1)

        store.updateTab(TabType.AGENT, 'a1', {})
        await flush()
        expect(runs).toBe(1)
        dispose()
        done()
      })
    })
  })

  it('updateTab: undefined-valued field that matches prev (also undefined) is a no-op', async () => {
    // A common pattern in useWorkspaceConnection's payload assembly is
    // `...(cond ? { x: undefined } : {})`. When prev[x] is also
    // undefined the supplied value is a no-op and the row must stay.
    await new Promise<void>((done) => {
      createRoot(async (dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1' })

        let runs = 0
        createEffect(() => {
          void store.state.tabs.find(t => t.id === 'a1')?.startupError
          runs++
        })
        await flush()
        expect(runs).toBe(1)

        store.updateTab(TabType.AGENT, 'a1', { startupError: undefined })
        await flush()
        expect(runs).toBe(1)
        dispose()
        done()
      })
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

  it('derives merged MRU from per-tab activation order after reassign', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      // Tile A's MRU: a1, a2 — after addTab calls.
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 't-a' }) // a1 active in t-a
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 't-a' }) // a2 active, MRU=[a2,a1]
      // Tile B has its own MRU.
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 't-b' })
      // Reassign t-a, t-b → t-merged.
      store.reassignTabsToTile(['t-a', 't-b'], 't-merged')
      // Per-tile MRU follows tile_id automatically: every tab is now in
      // t-merged, ordered by per-tab activation timestamp (latest first).
      expect(store.getTileMruOrder('t-merged')).toEqual(['2:t1', '1:a2', '1:a1'])
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
      expect(store.getTileMruOrder('t-a')).toEqual([])
      expect(store.state.tileActiveTabKeys['t-b']).toBeUndefined()
      expect(store.getTileMruOrder('t-b')).toEqual([])
      dispose()
    })
  })

  // Regression: pre-fix, when the projection-reconciler ran ahead of
  // reassignTabsToTile and pre-set tileActiveTabKeys[newTileId] = tab
  // key (the "grid → tile" race that happens because emitReplaceGrid
  // WithLeaf's bumpPending fires Solid effects synchronously before
  // the next call returns), reassignTabsToTile then overwrote the
  // newTile's per-tile state with empty data sourced from the
  // already-cleaned source cells. Net effect: user converted a grid
  // back to a tile, agent landed on the parent leaf, but no tab was
  // focused because tileActiveTabKeys[parent] had been zeroed.
  it('preserves existing newTileId active/mru when the reconciler set it ahead of us', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      // Simulate the reconciler's effect: tab already on the destination
      // tile with active/mru registered, while the source cells are
      // empty placeholders (their tabs were moved out by an earlier
      // reconcile pass).
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 't-merged' })
      // Touch source tiles' empty entries so they exist in the maps
      // (mimics the reconciler's detach step that left them as []/null).
      store.addTab({ type: TabType.AGENT, id: 'tmp', tileId: 't-a' })
      store.removeTab(TabType.AGENT, 'tmp')

      store.reassignTabsToTile(['t-a', 't-b'], 't-merged')

      // The existing active key on t-merged survives the reassign.
      expect(store.state.tileActiveTabKeys['t-merged']).toBe('1:a1')
      expect(store.getTileMruOrder('t-merged')).toEqual(['1:a1'])
      dispose()
    })
  })
})

// Regression: pre-fix, `reconcileFromProjection` step 3 updated
// `tab.tileId` to match the projection but left `tileActiveTabKeys`
// pinned to the OLD tile. After splitTile migrated a tab from parent
// to childA, childA's tile-active was empty so `getActiveTabForTile
// (childA)` returned null and the new leaf rendered no active tab.
// The fix: per-tile MRU is derived from `tab.tileId`, so it auto-
// migrates; `getActiveTabKeyForTile` validates the stored active key
// against the tab's current tile, so a stale entry doesn't leak.
describe('reconcileFromProjection tile MRU migration', () => {
  it('migrates per-tile active/mru when a tab tileId changes', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'parent' })
      expect(store.state.tileActiveTabKeys.parent).toBe('1:a1')

      store.reconcileFromProjection({
        workspaceId: 'ws-1',
        renderedTabs: [{
          tabType: TabType.AGENT,
          tabId: 'a1',
          tileId: 'childA',
          position: 'M',
          workerId: '',
        }],
        crdtKnownTabIds: new Set(['a1']),
      })

      const moved = store.state.tabs.find(t => t.id === 'a1')
      expect(moved?.tileId).toBe('childA')
      // New tile picks up the migrated tab as its active.
      expect(store.getActiveTabKeyForTile('childA')).toBe('1:a1')
      expect(store.getTileMruOrder('childA')).toEqual(['1:a1'])
      // Old tile no longer claims the tab as active — the stored
      // active key from the parent's pre-migration state references a
      // tab that's now in childA, so the validator-aware accessor
      // returns null.
      expect(store.getActiveTabKeyForTile('parent')).toBeNull()
      dispose()
    })
  })

  // Regression: the cross-workspace move bug. The reconciler effect in
  // AppShell used to read `state.tabs` inside `reconcileFromProjection`
  // without an `untrack`, so any optimistic `tabStore.addTab` would
  // re-run the effect against a CRDT projection that hadn't been
  // updated yet — and step 1 would silently remove the just-added
  // tab as "gone from this workspace". When the canonical move op
  // finally landed, step 2 would re-add the tab as a bare record with
  // no title / agentProvider / git fields. Symptom: dragging a tab
  // from another workspace's sidebar entry into the active workspace
  // showed it as its nanoid + generic icon until page refresh.
  //
  // This test pins the underlying reconciler contract: a tab the local
  // store has but the projection doesn't yet know about (because its
  // CRDT op hasn't been emitted) is preserved when `crdtKnownTabIds`
  // doesn't yet list it.
  it('preserves a locally-added tab when CRDT does not yet know its id', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      // Pre-existing tab Y (the target workspace's existing tab).
      store.addTab({
        type: TabType.AGENT,
        id: 'Y',
        tileId: 'tile-target',
        title: 'Agent Leah',
        workerId: 'w-1',
      })
      // Optimistically-added tab X (just moved in from another workspace).
      // Its move op hasn't been emitted yet, so the CRDT speculative
      // state knows nothing about it in this workspace.
      store.addTab({
        type: TabType.AGENT,
        id: 'X',
        tileId: 'tile-target',
        title: 'Agent Sullivan',
        workerId: 'w-1',
      }, { activate: false })

      store.reconcileFromProjection({
        workspaceId: 'ws-target',
        renderedTabs: [{
          tabType: TabType.AGENT,
          tabId: 'Y',
          tileId: 'tile-target',
          position: 'N',
          workerId: 'w-1',
        }],
        // CRDT does NOT yet know about X — its move op hasn't been
        // emitted. Without this guard the reconciler would have wiped X.
        crdtKnownTabIds: new Set(['Y']),
      })

      const x = store.state.tabs.find(t => t.id === 'X')
      expect(x).toBeDefined()
      expect(x?.title).toBe('Agent Sullivan')
      dispose()
    })
  })

  // Companion: once the CRDT projection catches up (move ops applied,
  // X now in renderedTabs for this workspace), step 3 should NOT
  // strip title / agentProvider — only tile_id / position / worker_id
  // are CRDT-driven fields.
  it('preserves title / agentProvider when step 3 syncs tile_id/position', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      const agentProvider = 1 // CLAUDE_CODE
      store.addTab({
        type: TabType.AGENT,
        id: 'X',
        tileId: 'tile-stale',
        position: 'M',
        title: 'Agent Sullivan',
        agentProvider,
        workerId: 'w-1',
      })

      store.reconcileFromProjection({
        workspaceId: 'ws-target',
        renderedTabs: [{
          tabType: TabType.AGENT,
          tabId: 'X',
          tileId: 'tile-new',
          position: 'P',
          workerId: 'w-1',
        }],
        crdtKnownTabIds: new Set(['X']),
      })

      const x = store.state.tabs.find(t => t.id === 'X')
      expect(x).toBeDefined()
      expect(x?.tileId).toBe('tile-new')
      expect(x?.position).toBe('P')
      // The non-CRDT fields survive the reconcile.
      expect(x?.title).toBe('Agent Sullivan')
      expect(x?.agentProvider).toBe(agentProvider)
      dispose()
    })
  })
})

describe('cleanupTiles', () => {
  it('drops active-tab entries for every passed tile id', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 't-a' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 't-b' })
      store.addTab({ type: TabType.AGENT, id: 'a3', tileId: 't-keep' })
      // Remove the actual tabs first so the per-tile active entries are
      // the only remaining bookkeeping (removeTab clears them anyway,
      // but we re-seed below to test the cleanup explicitly).
      store.removeTab(TabType.AGENT, 'a1')
      store.removeTab(TabType.AGENT, 'a2')
      const beforeKeepActive = store.state.tileActiveTabKeys['t-keep']

      store.cleanupTiles(['t-a', 't-b'])

      expect(store.state.tileActiveTabKeys['t-a']).toBeUndefined()
      expect(store.state.tileActiveTabKeys['t-b']).toBeUndefined()
      expect(store.state.tileActiveTabKeys['t-keep']).toBe(beforeKeepActive)
      // MRU follows tile_id automatically — the cleanup target tiles
      // never held any tabs after the remove calls, so their MRU is
      // already empty.
      expect(store.getTileMruOrder('t-a')).toEqual([])
      expect(store.getTileMruOrder('t-b')).toEqual([])
      dispose()
    })
  })

  it('is a no-op for an empty iterable', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 't-a' })
      const beforeActive = store.state.tileActiveTabKeys

      store.cleanupTiles([])

      // Same map reference — nothing was rewritten.
      expect(store.state.tileActiveTabKeys).toBe(beforeActive)
      dispose()
    })
  })

  it('cleanupTile delegates to cleanupTiles (single-id parity)', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 't-a' })
      store.removeTab(TabType.AGENT, 'a1')
      // After removeTab there are no tabs, but the active register was
      // last-set on insert and cleared by removeTab's fallback. Seed a
      // throwaway entry to make cleanup observable.
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 't-a' })
      expect(store.state.tileActiveTabKeys['t-a']).toBeDefined()

      store.cleanupTile('t-a')

      expect(store.state.tileActiveTabKeys['t-a']).toBeUndefined()
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
      // Source's per-tile state is cleared. MRU follows tile_id, so the
      // source tile naturally reports an empty list.
      expect(store.state.tileActiveTabKeys['t-source']).toBeUndefined()
      expect(store.getTileMruOrder('t-source')).toEqual([])
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

  it('derives target MRU from per-tab activation order after merge', () => {
    createRoot((dispose) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'tgt-a', tileId: 't-target' })
      store.addTab({ type: TabType.AGENT, id: 'tgt-b', tileId: 't-target' })
      store.addTab({ type: TabType.AGENT, id: 'src-a', tileId: 't-source' })
      store.addTab({ type: TabType.AGENT, id: 'src-b', tileId: 't-source' })

      store.mergeTabsIntoTile('t-source', 't-target')

      // After merge every tab lives on t-target; per-tile MRU is
      // sorted by activation order (latest first), so the most-
      // recently-added (src-b) leads.
      expect(store.getTileMruOrder('t-target')).toEqual(['1:src-b', '1:src-a', '1:tgt-b', '1:tgt-a'])
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
      expect(store.getTileMruOrder('t-source')).toEqual([])
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

  // isTabActiveAnywhere is the chat-trimming hot-path predicate: when
  // an incoming agent message lands and the tab isn't visible on any
  // tile (or globally), the chat store trims background history to
  // MAX_BACKGROUND_CHAT_MESSAGES. The store maintains an inverted
  // index (`tileIdsByActiveKey`) so this lookup is O(1) instead of
  // an O(N tiles) Object.values walk. These tests pin the index's
  // correctness across every mutator that touches active state.
  describe('isTabActiveAnywhere', () => {
    it('returns true when the tab is the global active', () => {
      createRoot((dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
        // addTab(activate: true) sets state.activeTabKey = 1:a1
        expect(store.isTabActiveAnywhere(TabType.AGENT, 'a1')).toBe(true)
        dispose()
      })
    })

    it('returns true when the tab is the per-tile active on at least one tile (and not global)', () => {
      createRoot((dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
        store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-2' })
        // a2's addTab activated it → state.activeTabKey === 1:a2;
        // explicitly steer global active off both candidates so the
        // "active on tile, not global" branch is exercised.
        store.setActiveTabForTile('tile-1', TabType.AGENT, 'a1')
        // Make a third tab the global active so a1 is per-tile-only.
        store.addTab({ type: TabType.AGENT, id: 'a3' })
        expect(store.state.activeTabKey).toBe('1:a3')
        expect(store.isTabActiveAnywhere(TabType.AGENT, 'a1')).toBe(true)
        dispose()
      })
    })

    it('returns false when the tab exists but is not active anywhere', () => {
      createRoot((dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
        store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-1' }, { activate: false })
        // a1 active on tile-1 and globally; a2 is neither.
        expect(store.isTabActiveAnywhere(TabType.AGENT, 'a2')).toBe(false)
        dispose()
      })
    })

    it('returns false for a tab that does not exist', () => {
      createRoot((dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
        expect(store.isTabActiveAnywhere(TabType.AGENT, 'nope')).toBe(false)
        dispose()
      })
    })

    it('flips to false once the tab is removed', () => {
      createRoot((dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
        store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-1' })
        // a2 is global active and tile-1 active; a1 still has its own
        // history but the inverted index reflects only a2 right now.
        expect(store.isTabActiveAnywhere(TabType.AGENT, 'a2')).toBe(true)
        store.removeTab(TabType.AGENT, 'a2')
        // removeTab promotes next-MRU on the tile — a1 becomes active.
        expect(store.isTabActiveAnywhere(TabType.AGENT, 'a2')).toBe(false)
        expect(store.isTabActiveAnywhere(TabType.AGENT, 'a1')).toBe(true)
        dispose()
      })
    })

    it('reflects a moveTabToTile — index updates from source tile to destination tile', () => {
      createRoot((dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
        // a1 is active on tile-1 and globally.
        expect(store.isTabActiveAnywhere(TabType.AGENT, 'a1')).toBe(true)
        store.moveTabToTile('1:a1', 'tile-2')
        // Per-tile active follows the move; isTabActiveAnywhere still
        // sees a1 (now on tile-2, plus still global active).
        expect(store.isTabActiveAnywhere(TabType.AGENT, 'a1')).toBe(true)
        // No leaked stale entry on tile-1: only check via the public
        // surface — moving the sole tab away from tile-1 dropped the
        // tileActiveTabKeys entry.
        expect(store.getActiveTabKeyForTile('tile-1')).toBeNull()
        dispose()
      })
    })

    it('drops the index entry after cleanupTile', () => {
      createRoot((dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
        store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-2' })
        // Make a1 per-tile-only (so we don't trivially pass via the
        // state.activeTabKey check).
        store.setActiveTabForTile('tile-1', TabType.AGENT, 'a1')
        store.addTab({ type: TabType.AGENT, id: 'a3' })
        expect(store.state.activeTabKey).toBe('1:a3')
        expect(store.isTabActiveAnywhere(TabType.AGENT, 'a1')).toBe(true)
        store.cleanupTile('tile-1')
        expect(store.isTabActiveAnywhere(TabType.AGENT, 'a1')).toBe(false)
        dispose()
      })
    })

    it('bulk cleanupTiles drops entries for every passed tile id at once', () => {
      createRoot((dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
        store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-2' })
        store.addTab({ type: TabType.AGENT, id: 'a3', tileId: 'tile-3' })
        store.setActiveTabForTile('tile-1', TabType.AGENT, 'a1')
        store.setActiveTabForTile('tile-2', TabType.AGENT, 'a2')
        store.setActiveTabForTile('tile-3', TabType.AGENT, 'a3')
        // Make a4 the global active so each ai is per-tile-only.
        store.addTab({ type: TabType.AGENT, id: 'a4' })
        store.cleanupTiles(['tile-1', 'tile-2'])
        expect(store.isTabActiveAnywhere(TabType.AGENT, 'a1')).toBe(false)
        expect(store.isTabActiveAnywhere(TabType.AGENT, 'a2')).toBe(false)
        // tile-3 untouched → a3 still active there.
        expect(store.isTabActiveAnywhere(TabType.AGENT, 'a3')).toBe(true)
        dispose()
      })
    })
  })

  // promoteNextActiveOnTile is the shared "tile lost its active tab,
  // pick the next-highest-MRU survivor" helper that removeTab and
  // moveTabToTile both call. Most of its happy-path coverage lives in
  // the existing per-mutator tests above; the cases here focus on
  // the contract boundaries — no-tabs-left, inactive-tab paths, and
  // the moveTabToTile-with-same-source-and-destination short-circuit.
  describe('promoteNextActiveOnTile via removeTab / moveTabToTile', () => {
    it('removeTab on the only tab on a tile clears tileActiveTabKeys[tile]', () => {
      createRoot((dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
        expect(store.getActiveTabKeyForTile('tile-1')).toBe('1:a1')
        store.removeTab(TabType.AGENT, 'a1')
        // No MRU candidate → tileActiveTabKeys[tile-1] is null;
        // the public getter normalizes that to null too.
        expect(store.getActiveTabKeyForTile('tile-1')).toBeNull()
        dispose()
      })
    })

    it('removeTab on an inactive tab leaves the tile\'s active untouched', () => {
      createRoot((dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
        store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-1' }, { activate: false })
        // a1 is the per-tile active. Remove a2 (not active) — a1
        // must stay active without any MRU re-evaluation.
        const beforeActive = store.getActiveTabKeyForTile('tile-1')
        store.removeTab(TabType.AGENT, 'a2')
        expect(store.getActiveTabKeyForTile('tile-1')).toBe(beforeActive)
        expect(store.getActiveTabKeyForTile('tile-1')).toBe('1:a1')
        dispose()
      })
    })

    it('moveTabToTile with target === source short-circuits the promote step', () => {
      createRoot((dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
        store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-1' })
        // a2 active on tile-1. Moving to the same tile must not
        // trigger the source-promote path (would otherwise pick a1
        // because the predicate `sourceTileId !== targetTileId`
        // gates it).
        store.moveTabToTile('1:a2', 'tile-1')
        expect(store.getActiveTabKeyForTile('tile-1')).toBe('1:a2')
        dispose()
      })
    })

    it('removeTab on the global active falls back to the next-MRU tab anywhere', () => {
      createRoot((dispose) => {
        const store = createTabStore()
        store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'tile-1' })
        store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'tile-2' })
        // a2 is global active and tile-2 active. After removeTab(a2):
        //  - tile-2 active: cleared (no tabs left on tile-2)
        //  - global active: falls back to a1 (next MRU)
        store.removeTab(TabType.AGENT, 'a2')
        expect(store.getActiveTabKeyForTile('tile-2')).toBeNull()
        expect(store.state.activeTabKey).toBe('1:a1')
        dispose()
      })
    })
  })
})
