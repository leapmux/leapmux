import type { BackgroundTaskItem as ProtoBackgroundTaskItem } from '~/generated/proto/leapmux/v1/agent_pb'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import { describe, expect, it } from 'vitest'
import { BackgroundTaskKind, BackgroundTaskStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import {
  backgroundTaskEndLabel,
  backgroundTaskEndTooltip,
  backgroundTaskStatusLabel,
  chipTasksFor,
  countActiveBackgroundTasks,
  createTabTaskScope,
  filterBackgroundTasksByKind,
  groupBackgroundTasks,
  isActiveBackgroundTaskStatus,
  opensSubagentTranscript,
  protoBackgroundTaskToStore,
  shouldShowBackgroundTasksSection,
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

describe('isActiveBackgroundTaskStatus', () => {
  it('active = pending|running', () => {
    expect(isActiveBackgroundTaskStatus('pending')).toBe(true)
    expect(isActiveBackgroundTaskStatus('running')).toBe(true)
  })

  it('every finished status is inactive', () => {
    for (const status of ['completed', 'failed', 'stopped', 'interrupted'] as const)
      expect(isActiveBackgroundTaskStatus(status)).toBe(false)
  })
})

describe('chipTasksFor', () => {
  const row = (over: Partial<BackgroundTaskItem> & { rowKey: string }): BackgroundTaskItem => ({
    kind: 'subagent',
    title: 't',
    activity: '',
    status: 'running',
    ...over,
  })

  // The registry is keyed by ROOT owner, so a child tab handed the whole thing
  // showed its PARENT's count -- siblings, and its own row, included.
  it('gives a child only the rows it spawned, never its own', () => {
    const tasks = [
      row({ rowKey: 'self', childAgentId: 'child-1', parentAgentId: 'root-1' }),
      row({ rowKey: 'sibling', childAgentId: 'child-2', parentAgentId: 'root-1' }),
      row({ rowKey: 'mine', parentAgentId: 'child-1' }),
      row({ rowKey: 'grandchild', parentAgentId: 'child-2' }),
    ]
    expect(chipTasksFor('child-1', tasks, true).map(t => t.rowKey)).toEqual(['mine'])
  })

  it('is empty for a child that spawned nothing', () => {
    const tasks = [row({ rowKey: 'self', childAgentId: 'child-1', parentAgentId: 'root-1' })]
    expect(chipTasksFor('child-1', tasks, true)).toEqual([])
  })

  // A root owns the registry, so its chip keeps rolling up every descendant --
  // the behaviour it has always had.
  it('gives a root the whole registry, descendants included', () => {
    const tasks = [
      row({ rowKey: 'direct', parentAgentId: 'root-1' }),
      row({ rowKey: 'grandchild', parentAgentId: 'child-1' }),
    ]
    expect(chipTasksFor('root-1', tasks, false).map(t => t.rowKey)).toEqual(['direct', 'grandchild'])
  })
})

describe('backgroundTaskStatusLabel', () => {
  it('names the in-progress states, which share one dot color', () => {
    expect(backgroundTaskStatusLabel('pending')).toBe('Pending')
    expect(backgroundTaskStatusLabel('running')).toBe('Running')
  })

  it('reuses the final end labels', () => {
    expect(backgroundTaskStatusLabel('completed')).toBe('Completed')
    expect(backgroundTaskStatusLabel('failed')).toBe('Failed')
    expect(backgroundTaskStatusLabel('stopped')).toBe('Stopped')
    expect(backgroundTaskStatusLabel('interrupted')).toBe('Interrupted')
  })
})

/**
 * The kind filter behind the list's All / Subagents / Shell tabs. A registry
 * that mixes subagents with background shells reads as one undifferentiated
 * list, and the two are looked for separately.
 */
describe('filterBackgroundTasksByKind', () => {
  const mixed = [
    item({ rowKey: 'a', kind: 'subagent' }),
    item({ rowKey: 's', kind: 'shell' }),
    item({ rowKey: 'b', kind: 'subagent' }),
  ]

  it('returns every row for `all`', () => {
    expect(filterBackgroundTasksByKind(mixed, 'all').map(t => t.rowKey)).toEqual(['a', 's', 'b'])
  })

  it('returns only the rows of the named kind', () => {
    expect(filterBackgroundTasksByKind(mixed, 'subagent').map(t => t.rowKey)).toEqual(['a', 'b'])
    expect(filterBackgroundTasksByKind(mixed, 'shell').map(t => t.rowKey)).toEqual(['s'])
  })

  it('keeps the input order within a kind', () => {
    const many = [
      item({ rowKey: 's1', kind: 'shell' }),
      item({ rowKey: 'a1', kind: 'subagent' }),
      item({ rowKey: 's2', kind: 'shell' }),
    ]
    expect(filterBackgroundTasksByKind(many, 'shell').map(t => t.rowKey)).toEqual(['s1', 's2'])
  })

  // `all` hands the same array back, so the identity an upstream memo
  // established survives -- the list's sort-and-group memo keys off it.
  it('returns the input array itself for `all`', () => {
    expect(filterBackgroundTasksByKind(mixed, 'all')).toBe(mixed)
  })

  it('returns an empty array when no row has that kind', () => {
    expect(filterBackgroundTasksByKind([item({ rowKey: 'a', kind: 'subagent' })], 'shell')).toEqual([])
    expect(filterBackgroundTasksByKind([], 'all')).toEqual([])
  })
})

/**
 * Whether the Background tasks section belongs on screen.
 *
 * The failure case is the one worth pinning: the section is hidden when the
 * registry is empty, so a worker that cannot answer used to render exactly like
 * an agent that had run nothing -- and the section left the screen with only a
 * warn in the worker log to explain it.
 */
describe('shouldShowBackgroundTasksSection', () => {
  it('shows the section for any row, finished ones included', () => {
    expect(shouldShowBackgroundTasksSection([item({ rowKey: 'a', status: 'running' })], false)).toBe(true)
    expect(shouldShowBackgroundTasksSection([item({ rowKey: 'a', status: 'completed' })], false)).toBe(true)
  })

  it('hides the section for an empty registry that loaded fine', () => {
    expect(shouldShowBackgroundTasksSection([], false)).toBe(false)
  })

  it('shows the section when the load failed, so the failure can say so', () => {
    expect(shouldShowBackgroundTasksSection([], true)).toBe(true)
  })

  // A failure with rows still shows: the rows are what the user came for, and
  // the flag only decides visibility, never what replaces the content.
  it('shows the section when the load failed and rows survive', () => {
    expect(shouldShowBackgroundTasksSection([item({ rowKey: 'a' })], true)).toBe(true)
  })
})

// One predicate for "is this row a link", read by the Background tasks list and
// by the SendMessage card. Each site covers only half of it today, so the cases
// live here.
describe('opensSubagentTranscript', () => {
  it('reports a subagent row that owns a transcript', () => {
    expect(opensSubagentTranscript(item({ rowKey: 'r1', kind: 'subagent', childAgentId: 'c1' }))).toBe(true)
  })

  it('reports a subagent whose provider never linked one', () => {
    expect(opensSubagentTranscript(item({ rowKey: 'r1', kind: 'subagent', childAgentId: undefined }))).toBe(false)
  })

  // The falsy-empty-string case, which is why the predicate tests truthiness
  // rather than `!== undefined`.
  it('reports a blank child id as no transcript', () => {
    expect(opensSubagentTranscript(item({ rowKey: 'r1', kind: 'subagent', childAgentId: '' }))).toBe(false)
  })

  it('reports a shell row as no transcript, whatever it carries', () => {
    expect(opensSubagentTranscript(item({ rowKey: 'r1', kind: 'shell', childAgentId: 'c1' }))).toBe(false)
  })
})

describe('createTabTaskScope', () => {
  const row = (over: Partial<BackgroundTaskItem> & { rowKey: string }): BackgroundTaskItem => ({
    kind: 'subagent',
    title: 't',
    activity: '',
    status: 'running',
    ...over,
  })

  // A registry keyed by ROOT owner, and two tabs reading it: `root-1` owns it,
  // `child-1` is a subagent transcript under it.
  //
  // `type` is not decoration: `isSubagentTab` -- which decides the child
  // scoping below -- tests it at runtime, so a fixture that omits it reads
  // `child-1` as a root. The helper tests `type` because `tabBusyProbe` hands
  // it an unnarrowed tab; the dep here is typed `AgentTab | undefined`, and the
  // real `getAgentTab` answers with an AGENT tab or with nothing.
  const tabs: Record<string, { type: TabType, id: string, parentAgentId?: string, rootAgentId?: string }> = {
    'root-1': { type: TabType.AGENT, id: 'root-1' },
    'child-1': { type: TabType.AGENT, id: 'child-1', parentAgentId: 'root-1', rootAgentId: 'root-1' },
  }
  const scope = (rows: BackgroundTaskItem[]) => createTabTaskScope({
    getAgentTab: id => tabs[id] as never,
    tasksForRoot: rootId => (rootId === 'root-1' ? rows : []),
  })

  it('resolves a child tab to its registry owner', () => {
    const s = scope([])
    expect(s.rootFor('child-1')).toBe('root-1')
    expect(s.rootFor('root-1')).toBe('root-1')
  })

  it('reads a root agent nothing knows about as its own owner', () => {
    // An optimistic tab, or one hydration has not reached yet. It owns no
    // registry, so resolving it to anything else would read a stranger's rows.
    expect(scope([]).rootFor('never-seen')).toBe('never-seen')
  })

  it('hands a child tab its OWNER\'s registry, not an empty one', () => {
    const rows = [row({ rowKey: 'mine', parentAgentId: 'child-1' })]
    // The whole reason the resolution exists: the dependency's `tasksForRoot('child-1')` answers
    // nothing, because only a root keys a registry.
    expect(scope(rows).rootTasksFor('child-1').map(t => t.rowKey)).toEqual(['mine'])
  })

  it('scopes a tab to the rows IT spawned, and a root to all of them', () => {
    const rows = [
      row({ rowKey: 'self', childAgentId: 'child-1', parentAgentId: 'root-1' }),
      row({ rowKey: 'mine', parentAgentId: 'child-1' }),
    ]
    const s = scope(rows)
    // One definition, shared by the chip on the tab and the close guard that
    // lists what a close would interrupt. The two must not disagree about
    // whose work a tab is running.
    expect(s.tasksForTab('child-1').map(t => t.rowKey)).toEqual(['mine'])
    expect(s.tasksForTab('root-1').map(t => t.rowKey)).toEqual(['self', 'mine'])
  })
})
