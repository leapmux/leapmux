import type { AgentTab, Tab } from '~/stores/tab.types'
import { describe, expect, it } from 'vitest'
import { TabType, WatchMode, WatchRejectionReason } from '~/generated/leapmux/v1/workspace_pb'
import {
  buildWatchPlans,
  isTabOnScreen,
  shouldRetryRejection,
  tabWatchMode,
  watchPlanKey,
} from './watchPlan'

function agent(overrides: Partial<Extract<Tab, { type: TabType.AGENT }>> = {}): Tab {
  return {
    type: TabType.AGENT,
    id: 'a1',
    workspaceId: 'ws-1',
    tileId: 'tile-1',
    workerId: 'w1',
    ...overrides,
  }
}

function terminal(overrides: Partial<Extract<Tab, { type: TabType.TERMINAL }>> = {}): Tab {
  return {
    type: TabType.TERMINAL,
    id: 't1',
    workspaceId: 'ws-1',
    tileId: 'tile-1',
    workerId: 'w1',
    ...overrides,
  }
}

function file(overrides: Partial<Extract<Tab, { type: TabType.FILE }>> = {}): Tab {
  return {
    type: TabType.FILE,
    id: 'f1',
    workspaceId: 'ws-1',
    tileId: 'tile-1',
    workerId: 'w1',
    ...overrides,
  }
}

describe('tabWatchMode', () => {
  const activeKey = (tileId: string) => (tileId === 'tile-1' ? '1:a1' : null)

  it('returns FULL for active workspace + tile-active tab', () => {
    expect(tabWatchMode(agent(), 'ws-1', activeKey)).toBe(WatchMode.FULL)
  })

  it('returns NOTIFY when the workspace is not active', () => {
    expect(tabWatchMode(agent(), 'ws-other', activeKey)).toBe(WatchMode.NOTIFY)
  })

  it('returns NOTIFY when the tab is not the tile active tab', () => {
    expect(tabWatchMode(agent({ id: 'a2' }), 'ws-1', activeKey)).toBe(WatchMode.NOTIFY)
  })
})

describe('isTabOnScreen', () => {
  // activeKey matches the production `${tab.type}:${tab.id}` shape (TabType.AGENT = 1).
  const activeKey = (tileId: string) => (tileId === 'tile-1' ? '1:a1' : null)

  it('is true for active workspace + tile-active tab', () => {
    expect(isTabOnScreen(agent(), 'ws-1', activeKey)).toBe(true)
  })

  it('is false when the workspace differs', () => {
    expect(isTabOnScreen(agent(), 'ws-other', activeKey)).toBe(false)
  })

  it('is false when the tab is not the tile active tab', () => {
    expect(isTabOnScreen(agent({ id: 'a2' }), 'ws-1', activeKey)).toBe(false)
  })

  it('is false when there is no tile placement', () => {
    expect(isTabOnScreen(agent({ tileId: undefined }), 'ws-1', activeKey)).toBe(false)
  })

  it('is false when activeWorkspaceId is null', () => {
    expect(isTabOnScreen(agent(), null, activeKey)).toBe(false)
  })
})

describe('buildWatchPlans', () => {
  it('feeds every on-screen tile in a split layout as FULL', () => {
    const tabs = [
      agent({ id: 'a1', tileId: 'tile-1' }),
      agent({ id: 'a2', tileId: 'tile-2', workerId: 'w1' }),
    ]
    const activeKey = (tileId: string) => {
      if (tileId === 'tile-1')
        return '1:a1'
      if (tileId === 'tile-2')
        return '1:a2'
      return null
    }
    const plans = buildWatchPlans(tabs, 'ws-1', activeKey)
    const plan = plans.get('w1')!
    expect(plan.agents).toHaveLength(2)
    expect(plan.agents.every(a => a.mode === WatchMode.FULL)).toBe(true)
  })

  it('excludes FILE tabs and tabs missing tileId or workerId', () => {
    const tabs = [
      file(),
      agent({ tileId: undefined }),
      agent({ id: 'a3', workerId: undefined }),
      agent({ id: 'a4' }),
    ]
    const activeKey = () => '1:a4'
    const plans = buildWatchPlans(tabs, 'ws-1', activeKey)
    expect(plans.get('w1')?.agents.map(a => a.agentId)).toEqual(['a4'])
  })

  it('includes terminal tabs in watch plans', () => {
    const tabs = [terminal()]
    const activeKey = () => '1:t1'
    const plans = buildWatchPlans(tabs, 'ws-1', activeKey)
    expect(plans.get('w1')?.terminals.map(t => t.terminalId)).toEqual(['t1'])
  })

  it('adds a NOTIFY root entry for a child tab so it receives root notification events', () => {
    // A root tab + a child tab on the same worker. The child has parentAgentId
    // set to the root.
    const tabs = [
      agent({ id: 'root-1', tileId: 'tile-1' }),
      agent({ id: 'child-1', tileId: 'tile-2', parentAgentId: 'root-1' }),
    ]
    const activeKey = (tileId: string) => {
      if (tileId === 'tile-1')
        return '1:root-1'
      if (tileId === 'tile-2')
        return '1:child-1'
      return null
    }
    const getAgentTab = (id: string): AgentTab | undefined =>
      tabs.find(t => t.id === id) as AgentTab | undefined
    const plans = buildWatchPlans(tabs, 'ws-1', activeKey, () => 0n, () => 0, () => false, getAgentTab)
    const agentIds = plans.get('w1')!.agents.map(a => ({ id: a.agentId, mode: a.mode }))
    // root-1 appears twice (its own FULL + the child-driven NOTIFY). The dedup
    // keeps it to one entry — the root's own tab already placed it.
    expect(agentIds.filter(a => a.id === 'root-1')).toHaveLength(1)
    expect(agentIds).toContainEqual({ id: 'child-1', mode: WatchMode.FULL })
  })

  it('adds a NOTIFY root entry when the root tab is NOT placed (child-only)', () => {
    // Only the child is placed; the root is absent from the tab list.
    const child = agent({ id: 'child-1', tileId: 'tile-1', parentAgentId: 'root-1' })
    // getAgentTab resolves the child to its root even though root-1 is not in
    // the tabs list (simulating a lookup that finds the root tab record).
    const getAgentTab = (id: string): AgentTab | undefined => {
      if (id === 'child-1')
        return child as AgentTab
      if (id === 'root-1')
        return { ...child, id: 'root-1', parentAgentId: undefined } as AgentTab
      return undefined
    }
    const plans = buildWatchPlans([child], 'ws-1', () => '1:child-1', () => 0n, () => 0, () => false, getAgentTab)
    const agentIds = plans.get('w1')!.agents.map(a => ({ id: a.agentId, mode: a.mode }))
    // The child's own entry + a NOTIFY root entry.
    expect(agentIds).toContainEqual({ id: 'child-1', mode: WatchMode.FULL })
    expect(agentIds).toContainEqual({ id: 'root-1', mode: WatchMode.NOTIFY })
  })

  it('dedupes the root entry when two children share a root', () => {
    const tabs = [
      agent({ id: 'child-a', tileId: 'tile-1', parentAgentId: 'root-1' }),
      agent({ id: 'child-b', tileId: 'tile-2', parentAgentId: 'root-1' }),
    ]
    const activeKey = (tileId: string) => {
      if (tileId === 'tile-1')
        return '1:child-a'
      if (tileId === 'tile-2')
        return '1:child-b'
      return null
    }
    const getAgentTab = (id: string): AgentTab | undefined => {
      if (id === 'root-1')
        return { type: TabType.AGENT, id: 'root-1' } as AgentTab
      return tabs.find(t => t.id === id) as AgentTab | undefined
    }
    const plans = buildWatchPlans(tabs, 'ws-1', activeKey, () => 0n, () => 0, () => false, getAgentTab)
    const rootEntries = plans.get('w1')!.agents.filter(a => a.agentId === 'root-1')
    expect(rootEntries).toHaveLength(1)
    expect(rootEntries[0].mode).toBe(WatchMode.NOTIFY)
  })
})

describe('watchPlanKey', () => {
  it('is stable when only cursors change and moves when a mode changes', () => {
    const a = {
      agents: [{ agentId: 'a1', mode: WatchMode.FULL, cursorSeq: BigInt(1) } as never],
      terminals: [] as never[],
      terminalResync: new Set<string>(),
    }
    const b = {
      agents: [{ agentId: 'a1', mode: WatchMode.FULL, cursorSeq: BigInt(99) } as never],
      terminals: [] as never[],
      terminalResync: new Set<string>(),
    }
    const c = {
      agents: [{ agentId: 'a1', mode: WatchMode.NOTIFY, cursorSeq: BigInt(99) } as never],
      terminals: [] as never[],
      terminalResync: new Set<string>(),
    }
    expect(watchPlanKey(a)).toBe(watchPlanKey(b))
    expect(watchPlanKey(a)).not.toBe(watchPlanKey(c))
  })

  it('moves when a terminal is flagged for resync, so the cold plan goes out', () => {
    const clean = {
      agents: [] as never[],
      terminals: [{ terminalId: 't1', afterOffset: BigInt(400), mode: WatchMode.FULL } as never],
      terminalResync: new Set<string>(),
    }
    const flagged = {
      agents: [] as never[],
      terminals: [{ terminalId: 't1', afterOffset: BigInt(0), mode: WatchMode.FULL } as never],
      terminalResync: new Set(['t1']),
    }
    // Without the resync arm in the key, both plans would key identically
    // (`t1:FULL`) and the stream would never re-send the cold afterOffset.
    expect(watchPlanKey(clean)).not.toBe(watchPlanKey(flagged))
  })
})

describe('terminal resync plans', () => {
  it('subscribes a flagged terminal cold and records it in the plan', () => {
    const tab = {
      type: TabType.TERMINAL,
      id: 't1',
      workerId: 'w1',
      tileId: 'tile-1',
      position: 'p1',
    } as never
    const plans = buildWatchPlans(
      [tab],
      'ws-1',
      () => '1:t1',
      () => 0n,
      () => 400,
      () => true,
    )
    const plan = plans.get('w1')!
    expect(plan.terminals).toHaveLength(1)
    expect(plan.terminals[0].afterOffset).toBe(BigInt(0))
    expect(plan.terminalResync.has('t1')).toBe(true)
  })
})

describe('shouldRetryRejection', () => {
  it('retries LOOKUP_FAILED only when the tab still exists', () => {
    expect(shouldRetryRejection({ entityId: 'a1', reason: WatchRejectionReason.LOOKUP_FAILED } as never, true)).toBe(true)
    expect(shouldRetryRejection({ entityId: 'a1', reason: WatchRejectionReason.LOOKUP_FAILED } as never, false)).toBe(false)
  })

  it('settles NOT_FOUND and unrecognized reasons', () => {
    expect(shouldRetryRejection({ entityId: 'a1', reason: WatchRejectionReason.NOT_FOUND } as never, true)).toBe(false)
    expect(shouldRetryRejection({ entityId: 'a1', reason: WatchRejectionReason.UNSPECIFIED } as never, true)).toBe(false)
    expect(shouldRetryRejection({ entityId: 'a1', reason: 999 as WatchRejectionReason } as never, true)).toBe(false)
  })
})
