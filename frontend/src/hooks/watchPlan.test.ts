import type { Tab } from '~/stores/tab.types'
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
})

describe('watchPlanKey', () => {
  it('is stable when only cursors change and moves when a mode changes', () => {
    const a = {
      agents: [{ agentId: 'a1', mode: WatchMode.FULL, cursorSeq: BigInt(1) } as never],
      terminals: [] as never[],
    }
    const b = {
      agents: [{ agentId: 'a1', mode: WatchMode.FULL, cursorSeq: BigInt(99) } as never],
      terminals: [] as never[],
    }
    const c = {
      agents: [{ agentId: 'a1', mode: WatchMode.NOTIFY, cursorSeq: BigInt(99) } as never],
      terminals: [] as never[],
    }
    expect(watchPlanKey(a)).toBe(watchPlanKey(b))
    expect(watchPlanKey(a)).not.toBe(watchPlanKey(c))
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
