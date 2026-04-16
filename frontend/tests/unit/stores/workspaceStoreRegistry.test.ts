import type { WorkspaceSnapshot } from '~/stores/workspaceStoreRegistry'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createWorkspaceStoreRegistry } from '~/stores/workspaceStoreRegistry'

function makeSnapshot(workspaceId: string, overrides?: Partial<WorkspaceSnapshot>): WorkspaceSnapshot {
  return {
    workspaceId,
    tabs: [],
    activeTabKey: null,
    layout: {
      root: { type: 'leaf', id: 'tile-1' },
      focusedTileId: 'tile-1',
    },
    agents: [],
    restored: true,
    tabsLoaded: true,
    ...overrides,
  }
}

describe('createWorkspaceStoreRegistry', () => {
  it('should initialize empty', () => {
    createRoot((dispose) => {
      const registry = createWorkspaceStoreRegistry()
      expect(registry.all()).toEqual([])
      expect(registry.get('ws-1')).toBeUndefined()
      dispose()
    })
  })

  it('should store and retrieve snapshots', () => {
    createRoot((dispose) => {
      const registry = createWorkspaceStoreRegistry()
      const snap = makeSnapshot('ws-1')
      registry.set('ws-1', snap)

      expect(registry.get('ws-1')).toBe(snap)
      expect(registry.all()).toEqual([snap])
      dispose()
    })
  })

  it('should overwrite existing snapshots', () => {
    createRoot((dispose) => {
      const registry = createWorkspaceStoreRegistry()
      const snap1 = makeSnapshot('ws-1')
      const snap2 = makeSnapshot('ws-1', { restored: false })

      registry.set('ws-1', snap1)
      registry.set('ws-1', snap2)

      expect(registry.get('ws-1')).toBe(snap2)
      dispose()
    })
  })

  it('should handle multiple workspaces', () => {
    createRoot((dispose) => {
      const registry = createWorkspaceStoreRegistry()
      registry.set('ws-1', makeSnapshot('ws-1'))
      registry.set('ws-2', makeSnapshot('ws-2'))
      registry.set('ws-3', makeSnapshot('ws-3'))

      expect(registry.all()).toHaveLength(3)
      dispose()
    })
  })

  it('should preserve snapshot tabs data', () => {
    createRoot((dispose) => {
      const registry = createWorkspaceStoreRegistry()
      const snap = makeSnapshot('ws-1', {
        tabs: [
          {
            type: TabType.AGENT,
            id: 'agent-1',
            title: 'Test Agent',
            position: 'a',
            tileId: 'tile-1',
            workerId: 'worker-1',
          },
        ],
        activeTabKey: `${TabType.AGENT}:agent-1`,
        mruOrder: [`${TabType.AGENT}:agent-1`],
        tileActiveTabKeys: { 'tile-1': `${TabType.AGENT}:agent-1` },
        tileMruOrder: { 'tile-1': [`${TabType.AGENT}:agent-1`] },
      })

      registry.set('ws-1', snap)
      const retrieved = registry.get('ws-1')!

      expect(retrieved.tabs).toHaveLength(1)
      expect(retrieved.tabs[0].type).toBe(TabType.AGENT)
      expect(retrieved.tabs[0].id).toBe('agent-1')
      expect(retrieved.activeTabKey).toBe(`${TabType.AGENT}:agent-1`)
      dispose()
    })
  })

  it('update applies a patcher to an existing snapshot', () => {
    createRoot((dispose) => {
      const registry = createWorkspaceStoreRegistry()
      registry.set('ws-1', makeSnapshot('ws-1'))

      registry.update('ws-1', snap => ({ ...snap, tabsLoaded: false }))

      expect(registry.get('ws-1')?.tabsLoaded).toBe(false)
      dispose()
    })
  })

  it('update is a no-op when workspace has no snapshot', () => {
    createRoot((dispose) => {
      const registry = createWorkspaceStoreRegistry()
      registry.update('ws-1', snap => ({ ...snap, tabsLoaded: false }))
      expect(registry.get('ws-1')).toBeUndefined()
      dispose()
    })
  })

  it('findContaining returns the first snapshot matching the predicate', () => {
    createRoot((dispose) => {
      const registry = createWorkspaceStoreRegistry()
      registry.set('ws-1', makeSnapshot('ws-1'))
      registry.set('ws-2', makeSnapshot('ws-2', { tabsLoaded: false }))

      expect(registry.findContaining(s => !s.tabsLoaded)?.workspaceId).toBe('ws-2')
      expect(registry.findContaining(() => false)).toBeUndefined()
      dispose()
    })
  })
})
