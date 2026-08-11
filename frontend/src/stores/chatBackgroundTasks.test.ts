import type { BackgroundTaskItem as ProtoBackgroundTaskItem } from '~/generated/leapmux/v1/agent_pb'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import { describe, expect, it } from 'vitest'
import { BackgroundTaskKind, BackgroundTaskStatus } from '~/generated/leapmux/v1/agent_pb'
import {
  backgroundTaskEndLabel,
  backgroundTaskEndTooltip,
  backgroundTaskStatusLabel,
  countActiveBackgroundTasks,
  countActiveSubagentTask,
  groupBackgroundTasks,
  isActiveBackgroundTaskStatus,
  isTerminalBackgroundTaskStatus,
  protoBackgroundTaskToStore,
  sortBackgroundTasks,
} from '~/stores/chatBackgroundTasks'

function proto(over: { id: string, kind?: BackgroundTaskKind, status: BackgroundTaskStatus, title?: string, activeForm?: string }): ProtoBackgroundTaskItem {
  return {
    id: over.id,
    kind: over.kind ?? BackgroundTaskKind.SUBAGENT,
    status: over.status,
    title: over.title ?? '',
    activeForm: over.activeForm ?? '',
    childAgentId: '',
    parentAgentId: '',
    groupKey: '',
    groupLabel: '',
    description: '',
    createdAt: '',
    updatedAt: '',
    endedAt: '',
  } as ProtoBackgroundTaskItem
}

function item(over: Partial<BackgroundTaskItem> & { rowKey: string }): BackgroundTaskItem {
  return {
    kind: over.kind ?? 'subagent',
    title: over.title ?? 'T',
    activity: over.activity ?? '',
    status: over.status ?? 'running',
    ...over,
  }
}

describe('protoBackgroundTaskToStore', () => {
  it('maps enum statuses to the union', () => {
    expect(protoBackgroundTaskToStore(proto({ id: 'r1', status: BackgroundTaskStatus.RUNNING, title: 't', activeForm: 'a' })))
      .toMatchObject({ rowKey: 'r1', kind: 'subagent', status: 'running', activity: 'a' })
  })

  it('maps shell kind', () => {
    const got = protoBackgroundTaskToStore(proto({ id: 'r1', kind: BackgroundTaskKind.SHELL, status: BackgroundTaskStatus.COMPLETED }))
    expect(got.kind).toBe('shell')
    expect(got.status).toBe('completed')
  })

  it('collapses empty optionals to undefined', () => {
    const got = protoBackgroundTaskToStore(proto({ id: 'r1', status: BackgroundTaskStatus.PENDING, title: 't' }))
    expect(got.childAgentId).toBeUndefined()
    expect(got.groupKey).toBeUndefined()
  })
})

describe('countActiveBackgroundTasks', () => {
  it('counts pending + running only', () => {
    const items = [
      item({ rowKey: '1', status: 'running' }),
      item({ rowKey: '2', status: 'pending' }),
      item({ rowKey: '3', status: 'completed' }),
      item({ rowKey: '4', status: 'failed' }),
    ]
    expect(countActiveBackgroundTasks(items)).toBe(2)
  })
})

describe('sortBackgroundTasks', () => {
  it('active first, running before pending, then terminal', () => {
    const items = [
      item({ rowKey: 'completed', status: 'completed' }),
      item({ rowKey: 'pending', status: 'pending' }),
      item({ rowKey: 'running', status: 'running' }),
    ]
    const sorted = sortBackgroundTasks(items)
    expect(sorted.map(i => i.rowKey)).toEqual(['running', 'pending', 'completed'])
  })
})

describe('groupBackgroundTasks', () => {
  it('ungrouped first, then groups in first-seen order', () => {
    const items = [
      item({ rowKey: 'g1a', groupKey: 'g1', groupLabel: 'One' }),
      item({ rowKey: 'free' }),
      item({ rowKey: 'g1b', groupKey: 'g1', groupLabel: 'One' }),
      item({ rowKey: 'g2a', groupKey: 'g2', groupLabel: 'Two' }),
    ]
    const grouped = groupBackgroundTasks(items)
    expect(grouped.ungrouped.map(i => i.rowKey)).toEqual(['free'])
    expect(grouped.groups.map(g => g.key)).toEqual(['g1', 'g2'])
    expect(grouped.groups[0].items.map(i => i.rowKey)).toEqual(['g1a', 'g1b'])
  })
})

describe('backgroundTaskEndLabel', () => {
  it('labels each terminal status', () => {
    expect(backgroundTaskEndLabel('completed')).toBe('Completed')
    expect(backgroundTaskEndLabel('failed')).toBe('Failed')
    expect(backgroundTaskEndLabel('stopped')).toBe('Stopped')
    expect(backgroundTaskEndLabel('interrupted')).toBe('Interrupted')
    expect(backgroundTaskEndLabel('running')).toBe('')
  })
})

describe('backgroundTaskEndTooltip', () => {
  it('explains that interrupted means a worker restart', () => {
    expect(backgroundTaskEndTooltip('interrupted')).toBe('stopped by a worker restart')
  })

  it('returns undefined for statuses whose label is self-explanatory', () => {
    expect(backgroundTaskEndTooltip('completed')).toBeUndefined()
    expect(backgroundTaskEndTooltip('failed')).toBeUndefined()
    expect(backgroundTaskEndTooltip('stopped')).toBeUndefined()
    expect(backgroundTaskEndTooltip('running')).toBeUndefined()
  })
})

describe('isActive/isTerminal', () => {
  it('active = pending|running', () => {
    expect(isActiveBackgroundTaskStatus('pending')).toBe(true)
    expect(isActiveBackgroundTaskStatus('running')).toBe(true)
    expect(isActiveBackgroundTaskStatus('completed')).toBe(false)
  })

  it('terminal = completed|failed|stopped|interrupted', () => {
    expect(isTerminalBackgroundTaskStatus('completed')).toBe(true)
    expect(isTerminalBackgroundTaskStatus('interrupted')).toBe(true)
    expect(isTerminalBackgroundTaskStatus('running')).toBe(false)
  })
})

describe('countActiveSubagentTask', () => {
  const row = (over: Partial<BackgroundTaskItem>): BackgroundTaskItem => ({
    rowKey: 'r',
    kind: 'subagent',
    title: 't',
    activity: '',
    status: 'running',
    ...over,
  })

  it('counts this child while its own row is running', () => {
    expect(countActiveSubagentTask('child-1', [row({ childAgentId: 'child-1' })])).toBe(1)
  })

  it('counts a pending row as active', () => {
    expect(countActiveSubagentTask('child-1', [row({ childAgentId: 'child-1', status: 'pending' })])).toBe(1)
  })

  it('drops to zero once this child reaches a terminal status', () => {
    for (const status of ['completed', 'failed', 'stopped', 'interrupted'] as const)
      expect(countActiveSubagentTask('child-1', [row({ childAgentId: 'child-1', status })])).toBe(0)
  })

  // The whole point: a sibling still working must not keep this child's
  // indicator alive.
  it('ignores a sibling subagent that is still running', () => {
    expect(countActiveSubagentTask('child-1', [
      row({ rowKey: 'a', childAgentId: 'child-1', status: 'completed' }),
      row({ rowKey: 'b', childAgentId: 'child-2', status: 'running' }),
    ])).toBe(0)
  })

  it('answers zero when this child has no row yet', () => {
    expect(countActiveSubagentTask('child-1', [])).toBe(0)
    expect(countActiveSubagentTask('child-1', [row({ childAgentId: 'child-2' })])).toBe(0)
  })

  it('never counts more than the one row, even with duplicates', () => {
    expect(countActiveSubagentTask('child-1', [
      row({ rowKey: 'a', childAgentId: 'child-1' }),
      row({ rowKey: 'b', childAgentId: 'child-1' }),
    ])).toBe(1)
  })
})

describe('backgroundTaskStatusLabel', () => {
  it('names the in-progress states, which share one dot color', () => {
    expect(backgroundTaskStatusLabel('pending')).toBe('Pending')
    expect(backgroundTaskStatusLabel('running')).toBe('Running')
  })

  it('reuses the terminal end labels', () => {
    expect(backgroundTaskStatusLabel('completed')).toBe('Completed')
    expect(backgroundTaskStatusLabel('failed')).toBe('Failed')
    expect(backgroundTaskStatusLabel('stopped')).toBe('Stopped')
    expect(backgroundTaskStatusLabel('interrupted')).toBe('Interrupted')
  })
})
