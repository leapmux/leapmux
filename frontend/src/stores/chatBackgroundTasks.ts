import type { AgentTab } from './tab.types'
import type { BackgroundTaskItem as ProtoBackgroundTaskItem } from '~/generated/proto/leapmux/v1/agent_pb'
import { BackgroundTaskKind, BackgroundTaskStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { isSubagentTab, rootAgentIdFor } from './tab.helpers'

// ---------------------------------------------------------------------------
// Provider-neutral background-task registry model + conversions
//
// The store-shape BackgroundTaskItem and the helpers that normalize the proto
// wire form into it, plus the sort/group/count helpers the sidebar and the
// close guard share. Near-leaf: it imports the generated proto types plus
// `rootAgentIdFor`, for the one helper that has to resolve a child tab's
// registry owner -- so the chat store, the sidebar section, and the
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
  /**
   * Whether `title` is a verbatim shell command rather than prose, so the row
   * can set it as code. Only a provider that hands the command over ITSELF
   * reports it: an ACP terminal/create carries the command and nothing else,
   * while Claude's task_started carries `description || command`, so a Claude
   * shell row's title is prose whenever the model wrote any and nothing says
   * which it was. False is the safe answer.
   */
  titleIsCommand?: boolean
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
    titleIsCommand: t.titleIsCommand || undefined,
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

/**
 * Narrow a background-task status WIRE string to the union.
 *
 * The worker persists the subagent-end divider with the same four final wire
 * strings the registry uses (`bgtask.StatusWire`), so the divider's renderer
 * narrows through here rather than re-listing them. `normalizeBackgroundTaskStatus`
 * cannot serve: it maps the proto ENUM, not the JSON string.
 */
export function backgroundTaskStatusFromWire(s: string): BackgroundTaskItem['status'] | undefined {
  switch (s) {
    case 'pending':
    case 'running':
    case 'completed':
    case 'failed':
    case 'stopped':
    case 'interrupted':
      return s
    default:
      return undefined
  }
}

export function isActiveBackgroundTaskStatus(s: BackgroundTaskItem['status']): boolean {
  return s === 'pending' || s === 'running'
}

/**
 * Whether the Background tasks section belongs on screen.
 *
 * ANY row keeps it alive, finished ones included -- reading what a subagent did
 * after it ended is a first-class use case, so this is the registry's size, not
 * its active count.
 *
 * A failed LOAD keeps it alive too, and that half is the one worth stating: the
 * section is hidden when the registry is empty, so a worker that cannot answer
 * renders identically to an agent that has run nothing, and the section leaves
 * the screen with nothing to say why. A worker database missing a column did
 * exactly that, and the only trace was a warn in the worker log.
 *
 * Here rather than inline in the shell, so the rule sits with the registry's
 * other rules and can be tested without mounting AppShell.
 */
export function shouldShowBackgroundTasksSection(
  tasks: BackgroundTaskItem[],
  loadFailed: boolean,
): boolean {
  return tasks.length > 0 || loadFailed
}

/**
 * Which kind of row the background-task list shows: one kind, or every kind.
 *
 * Derived from the row's own `kind` rather than spelled out, so a third kind
 * reaches the filter (and its tab) by adding it to `BackgroundTaskItem` alone.
 */
export type BackgroundTaskKindFilter = 'all' | BackgroundTaskItem['kind']

// filterBackgroundTasksByKind returns the rows the given tab shows. `all`
// returns the input array itself, so the identity a memo upstream established
// survives the filter.
export function filterBackgroundTasksByKind(
  items: BackgroundTaskItem[],
  filter: BackgroundTaskKindFilter,
): BackgroundTaskItem[] {
  return filter === 'all' ? items : items.filter(it => it.kind === filter)
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
 * The rows a tab's background-tasks CHIP should show: the work that tab is
 * running, never the tab itself.
 *
 * A root owns the registry, so it sees every descendant's row -- that roll-up
 * is what the chip on a root tab has always meant. A child sees only the rows
 * IT spawned (`parentAgentId` identifies the immediate parent), which excludes its
 * own row by construction: that row belongs to its parent. Without the scoping
 * a subagent tab read its PARENT's count, siblings and itself included.
 */
export function chipTasksFor(
  agentId: string,
  rootTasks: BackgroundTaskItem[],
  isChild: boolean,
): BackgroundTaskItem[] {
  return isChild ? rootTasks.filter(t => t.parentAgentId === agentId) : rootTasks
}

/**
 * The registry rows that belong to ONE tab, resolving the tab's registry owner
 * on the way. Two callers need this and must not disagree: the chip on the tab,
 * and the close guard that lists what a close would interrupt.
 *
 * The scoping rule is chipTasksFor's -- a root sees every descendant's row, a
 * child sees only the rows IT spawned -- and the root resolution is what makes
 * a child tab read its owner's registry rather than an empty one.
 */
export function createTabTaskScope(deps: {
  getAgentTab: (id: string) => AgentTab | undefined
  tasksForRoot: (rootAgentId: string) => BackgroundTaskItem[]
}) {
  const rootFor = (agentId: string): string => rootAgentIdFor(deps.getAgentTab, agentId)
  return {
    rootFor,
    // Named for what it does rather than for the dependency it calls: the
    // dependency takes a ROOT id, while this takes ANY tab's agent id and
    // resolves the owner first. Sharing one name for both meanings made every
    // reader decide which one they were looking at.
    rootTasksFor: (agentId: string) => deps.tasksForRoot(rootFor(agentId)),
    tasksForTab: (agentId: string) => chipTasksFor(
      agentId,
      deps.tasksForRoot(rootFor(agentId)),
      isSubagentTab(deps.getAgentTab(agentId)),
    ),
  }
}

/** One tab-scoped view of the background-task registry. */
export type TabTaskScope = ReturnType<typeof createTabTaskScope>

/**
 * Whether this row owns a subagent transcript that a click can open. A shell row
 * owns none, and a subagent whose provider never linked one owns none either.
 * Shared, so the Background tasks list and the SendMessage card cannot disagree
 * about which rows are a link.
 *
 * The row property only. Whether the HOST supplied an open handler is the call
 * site's own question, and folding it in here would make a pure registry
 * predicate depend on a component's props.
 */
export function opensSubagentTranscript(item: BackgroundTaskItem): boolean {
  return item.kind === 'subagent' && !!item.childAgentId
}

// backgroundTaskStatusLabel spells a status out in full. The row shows status as a
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

// backgroundTaskEndLabel renders the final-status secondary line.
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
// final status, or undefined when the label alone is clear enough. The
// interrupted status is a worker/agent-process restart cutting the task off,
// which is not obvious from the bare "Interrupted" label.
export function backgroundTaskEndTooltip(s: BackgroundTaskItem['status']): string | undefined {
  if (s === 'interrupted')
    return 'stopped by a worker restart'
  return undefined
}

// sortBackgroundTasks returns a NEW array ordered active-first (running before
// pending), then finished; stable within each half (input order preserved).
export function sortBackgroundTasks(items: BackgroundTaskItem[]): BackgroundTaskItem[] {
  const active: BackgroundTaskItem[] = []
  const finished: BackgroundTaskItem[] = []
  for (const it of items) {
    if (isActiveBackgroundTaskStatus(it.status))
      active.push(it)
    else
      finished.push(it)
  }
  // Running before pending, stable.
  active.sort((a, b) => {
    const ar = a.status === 'running' ? 0 : 1
    const br = b.status === 'running' ? 0 : 1
    return ar - br
  })
  return [...active, ...finished]
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
