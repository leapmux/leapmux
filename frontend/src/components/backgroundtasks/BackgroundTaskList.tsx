import type { Component, JSX } from 'solid-js'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import Bot from 'lucide-solid/icons/bot'
import Circle from 'lucide-solid/icons/circle'
import Terminal from 'lucide-solid/icons/terminal'
import { createMemo, For, Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import {
  backgroundTaskEndLabel,
  backgroundTaskEndTooltip,
  backgroundTaskStatusLabel,
  groupBackgroundTasks,
  isActiveBackgroundTaskStatus,
  sortBackgroundTasks,
} from '~/stores/chatBackgroundTasks'
import * as styles from './BackgroundTaskList.css'

interface BackgroundTaskListProps {
  tasks: BackgroundTaskItem[]
  onOpenSubagent?: (item: BackgroundTaskItem) => void
  /** Extra class for the root list (e.g. the popover scroll variant). */
  class?: string
}

/** The status palette: in progress, succeeded, failed. */
function statusDotClass(status: BackgroundTaskItem['status']): string {
  switch (status) {
    case 'completed':
      return styles.statusDotSuccess
    // A crash cut the task off mid-flight, so it did not succeed. A user's
    // explicit stop is not a failure and stays muted.
    case 'failed':
    case 'interrupted':
      return styles.statusDotDanger
    case 'stopped':
      return styles.statusDotMuted
    default:
      return styles.statusDotActive
  }
}

/**
 * BackgroundTaskList renders the background-task registry for the sidebar
 * section and the ThinkingIndicator popover. Shared by both surfaces. Sorts
 * active-first (running before pending), groups by workflow/phase, and renders
 * a status glyph + title + secondary line per row. Subagent rows with a
 * childAgentId are clickable buttons; shell rows are static.
 */
export const BackgroundTaskList: Component<BackgroundTaskListProps> = (props) => {
  // Memoized so a broadcast tick re-runs sort+group once (not twice, once per
  // JSX read of `.ungrouped`/`.groups`), and only when props.tasks changes.
  const grouped = createMemo(() => groupBackgroundTasks(sortBackgroundTasks(props.tasks)))

  // Status reads as COLOR on one constant dot, not as a different glyph per
  // state. Six shapes made the column a legend to memorize; one dot in the
  // status palette (in progress / succeeded / failed) is legible at a glance and
  // lines the rows up. The exact state stays available as the dot's tooltip and
  // as the row's `data-status`, which is what tests and E2E select on.
  const statusGlyph = (item: BackgroundTaskItem): JSX.Element => (
    <Tooltip text={backgroundTaskStatusLabel(item.status)} ariaLabel>
      <span class={`${styles.taskIcon} ${statusDotClass(item.status)}`} data-testid="bg-task-status-dot">
        <Circle size={10} fill="currentColor" strokeWidth={0} />
      </span>
    </Tooltip>
  )

  const kindIcon = (item: BackgroundTaskItem): JSX.Element => {
    if (item.kind === 'shell')
      return <Terminal class={styles.taskIcon} size={14} />
    return <Bot class={styles.taskIcon} size={14} />
  }

  // The row's second line. It must never repeat the first: a provider whose
  // spawn payload carries one string for both (Claude's local_bash names the
  // command as the description, which is already the title) otherwise rendered
  // the same text twice. Neutral guard here rather than per provider, so the
  // next one to do it is covered too.
  const secondary = (item: BackgroundTaskItem): string => {
    const title = item.title || item.description || item.rowKey
    const text = isActiveBackgroundTaskStatus(item.status)
      ? item.activity || item.description || ''
      : backgroundTaskEndLabel(item.status)
    return text === title ? '' : text
  }

  // Explanatory tooltip for a terminal status whose bare label is ambiguous
  // (e.g. "Interrupted" really means the worker/agent process restarted).
  const secondaryTooltip = (item: BackgroundTaskItem): string | undefined => {
    if (isActiveBackgroundTaskStatus(item.status))
      return undefined
    return backgroundTaskEndTooltip(item.status)
  }

  const renderSecondary = (item: BackgroundTaskItem): JSX.Element => {
    const text = secondary(item)
    if (!text)
      return null
    const tip = secondaryTooltip(item)
    if (!tip)
      return <span class={styles.taskSecondary}>{text}</span>
    return (
      <Tooltip text={tip}>
        <span class={styles.taskSecondary}>{text}</span>
      </Tooltip>
    )
  }

  // The row's inner content and its data attributes are identical for both
  // element kinds; only the tag and the click handler differ. Building them once
  // keeps the clickable and static rows from drifting apart.
  const rowBody = (item: BackgroundTaskItem): JSX.Element => (
    <>
      {statusGlyph(item)}
      {kindIcon(item)}
      <div class={styles.taskBody}>
        <span class={styles.taskTitle}>{item.title || item.description || item.rowKey}</span>
        {renderSecondary(item)}
      </div>
    </>
  )

  // `extraClass` carries taskRowStatic for the non-clickable row, which drops
  // the pointer cursor taskRow sets for the clickable one.
  const rowAttrs = (item: BackgroundTaskItem, extraClass?: string) => ({
    'class': extraClass ? `${styles.taskRow} ${extraClass}` : styles.taskRow,
    'classList': { [styles.taskStruck]: !isActiveBackgroundTaskStatus(item.status) },
    'data-testid': 'bg-task-row',
    'data-status': item.status,
    'data-kind': item.kind,
    'data-child-agent-id': item.childAgentId ?? '',
  })

  const renderRow = (item: BackgroundTaskItem): JSX.Element => {
    const clickable = item.kind === 'subagent' && !!item.childAgentId && !!props.onOpenSubagent
    return (
      <Show
        when={clickable}
        fallback={<div {...rowAttrs(item, styles.taskRowStatic)}>{rowBody(item)}</div>}
      >
        <button type="button" {...rowAttrs(item)} onClick={() => props.onOpenSubagent?.(item)}>
          {rowBody(item)}
        </button>
      </Show>
    )
  }

  return (
    <div class={`${styles.taskList} ${props.class ?? ''}`}>
      <For each={grouped().ungrouped}>{item => renderRow(item)}</For>
      <For each={grouped().groups}>
        {group => (
          <>
            <div class={styles.groupHeader}>{group.label}</div>
            <For each={group.items}>{item => renderRow(item)}</For>
          </>
        )}
      </For>
    </div>
  )
}
