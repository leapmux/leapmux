import type { BackgroundTaskItem as ProtoBackgroundTaskItem } from '~/generated/leapmux/v1/agent_pb'
import { BackgroundTaskKind, BackgroundTaskStatus } from '~/generated/leapmux/v1/agent_pb'

// ---------------------------------------------------------------------------
// Provider-neutral background-task registry model + conversions
//
// The store-shape BackgroundTaskItem and the helpers that normalize the proto
// wire form into it, plus the sort/group/count helpers the sidebar and the
// ThinkingIndicator chips share. A leaf module -- it imports only the
// generated proto types -- so the chat store, the sidebar section, and the
// indicator chips share one shape without routing conversions through the
// window store.
// ---------------------------------------------------------------------------

export interface BackgroundTaskItem {
  rowKey: string
  kind: 'subagent' | 'shell'
  childAgentId?: string
  parentAgentId?: string
  groupKey?: string
  groupLabel?: string
  title: string
  description?: string
  activity: string
  status: 'pending' | 'running' | 'completed' | 'failed' | 'stopped' | 'interrupted'
  createdAt?: string
  updatedAt?: string
  endedAt?: string
}

export interface BackgroundTaskGroup {
  key: string
  label: string
  items: BackgroundTaskItem[]
}

export interface GroupedBackgroundTasks {
  ungrouped: BackgroundTaskItem[]
  groups: BackgroundTaskGroup[]
}

// protoBackgroundTaskToStore converts a wire item to the store shape, collapsing
// empty optionals to undefined so shallow-equal comparisons are stable.
export function protoBackgroundTaskToStore(t: ProtoBackgroundTaskItem): BackgroundTaskItem {
  return {
    rowKey: t.id,
    kind: t.kind === BackgroundTaskKind.SHELL ? 'shell' : 'subagent',
    childAgentId: t.childAgentId || undefined,
    parentAgentId: t.parentAgentId || undefined,
    groupKey: t.groupKey || undefined,
    groupLabel: t.groupLabel || undefined,
    title: t.title,
    description: t.description || undefined,
    activity: t.activeForm,
    status: normalizeBackgroundTaskStatus(t.status),
    createdAt: t.createdAt || undefined,
    updatedAt: t.updatedAt || undefined,
    endedAt: t.endedAt || undefined,
  }
}

function normalizeBackgroundTaskStatus(s: BackgroundTaskStatus): BackgroundTaskItem['status'] {
  switch (s) {
    case BackgroundTaskStatus.RUNNING:
      return 'running'
    case BackgroundTaskStatus.COMPLETED:
      return 'completed'
    case BackgroundTaskStatus.FAILED:
      return 'failed'
    case BackgroundTaskStatus.STOPPED:
      return 'stopped'
    case BackgroundTaskStatus.INTERRUPTED:
      return 'interrupted'
    default:
      return 'pending'
  }
}

export function isActiveBackgroundTaskStatus(s: BackgroundTaskItem['status']): boolean {
  return s === 'pending' || s === 'running'
}

export function isTerminalBackgroundTaskStatus(s: BackgroundTaskItem['status']): boolean {
  return s === 'completed' || s === 'failed' || s === 'stopped' || s === 'interrupted'
}

// countActiveBackgroundTasks returns the number of pending/running rows -- the
// figure the rail badge and the ThinkingIndicator chip render.
export function countActiveBackgroundTasks(items: BackgroundTaskItem[]): number {
  let n = 0
  for (const it of items) {
    if (isActiveBackgroundTaskStatus(it.status))
      n++
  }
  return n
}

/**
 * Whether THIS subagent is still running, as 0 or 1, read from its own row in
 * the root's registry.
 *
 * The registry is keyed by ROOT owner, so countActiveBackgroundTasks over it
 * answers "is any subagent of this root running" -- the right question for a
 * root agent's thinking indicator, and the wrong one for a child's: it kept a
 * FINISHED subagent's indicator spinning for as long as any SIBLING subagent
 * ran. Answers 0 for a child with no row yet (a spawn the provider has not
 * linked), which lets the caller's message-history heuristic decide instead of
 * asserting a negative.
 */
export function countActiveSubagentTask(childAgentId: string, rootTasks: BackgroundTaskItem[]): number {
  const own = rootTasks.find(t => t.childAgentId === childAgentId)
  return own && isActiveBackgroundTaskStatus(own.status) ? 1 : 0
}

// backgroundTaskStatusLabel names a status in full. The row shows status as a
// colored dot, so this is what the dot's tooltip (and its accessible name)
// says -- the color alone cannot distinguish failed from interrupted, or
// pending from running.
export function backgroundTaskStatusLabel(s: BackgroundTaskItem['status']): string {
  switch (s) {
    case 'pending':
      return 'Pending'
    case 'running':
      return 'Running'
    default:
      return backgroundTaskEndLabel(s)
  }
}

// backgroundTaskEndLabel renders the terminal-status secondary line.
export function backgroundTaskEndLabel(s: BackgroundTaskItem['status']): string {
  switch (s) {
    case 'completed':
      return 'Completed'
    case 'failed':
      return 'Failed'
    case 'stopped':
      return 'Stopped'
    case 'interrupted':
      return 'Interrupted'
    default:
      return ''
  }
}

// backgroundTaskEndTooltip returns an optional explanatory tooltip for a
// terminal status, or undefined when the label alone is clear enough. The
// interrupted status is a worker/agent-process restart cutting the task off,
// which is not obvious from the bare "Interrupted" label.
export function backgroundTaskEndTooltip(s: BackgroundTaskItem['status']): string | undefined {
  if (s === 'interrupted')
    return 'stopped by a worker restart'
  return undefined
}

// sortBackgroundTasks returns a NEW array ordered active-first (running before
// pending), then terminal; stable within each half (input order preserved).
export function sortBackgroundTasks(items: BackgroundTaskItem[]): BackgroundTaskItem[] {
  const active: BackgroundTaskItem[] = []
  const terminal: BackgroundTaskItem[] = []
  for (const it of items) {
    if (isActiveBackgroundTaskStatus(it.status))
      active.push(it)
    else
      terminal.push(it)
  }
  // Running before pending, stable.
  active.sort((a, b) => {
    const ar = a.status === 'running' ? 0 : 1
    const br = b.status === 'running' ? 0 : 1
    return ar - br
  })
  return [...active, ...terminal]
}

// groupBackgroundTasks splits sorted items into an ungrouped block first, then
// one block per group in first-seen order. Used by the sidebar list and the
// indicator popover.
export function groupBackgroundTasks(items: BackgroundTaskItem[]): GroupedBackgroundTasks {
  const ungrouped: BackgroundTaskItem[] = []
  const groups: BackgroundTaskGroup[] = []
  const indexByKey = new Map<string, number>()
  for (const it of items) {
    if (!it.groupKey) {
      ungrouped.push(it)
      continue
    }
    let gi = indexByKey.get(it.groupKey)
    if (gi === undefined) {
      gi = groups.length
      indexByKey.set(it.groupKey, gi)
      groups.push({ key: it.groupKey, label: it.groupLabel || it.groupKey, items: [] })
    }
    groups[gi].items.push(it)
  }
  return { ungrouped, groups }
}
