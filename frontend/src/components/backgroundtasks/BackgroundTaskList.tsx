import type { Component, JSX } from 'solid-js'
import type { BackgroundTaskItem } from '~/stores/chatBackgroundTasks'
import Bot from 'lucide-solid/icons/bot'
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
 * a kind icon, a title with its status dot floated to the end of the first
 * line, and a secondary line. Subagent rows with a childAgentId are clickable
 * buttons; shell rows are static.
 */
export const BackgroundTaskList: Component<BackgroundTaskListProps> = (props) => {
  // Memoized so a broadcast tick re-runs sort+group once (not twice, once per
  // JSX read of `.ungrouped`/`.groups`), and only when props.tasks changes.
  const grouped = createMemo(() => groupBackgroundTasks(sortBackgroundTasks(props.tasks)))

  // Status reads as COLOR on one constant dot, not as a different glyph per
  // state. Six shapes made the column a legend to memorize; one dot in the
  // status palette (in progress / succeeded / failed) is legible at a glance,
  // and an in-progress dot pulses so activity is visible without a spinner.
  // The exact state stays available as the dot's tooltip and as the row's
  // `data-status`, which is what tests and E2E select on.
  //
  // Rendered INSIDE the title so it can float to the right end of the title's
  // first line -- see statusDot in the stylesheet for why a float and not a
  // flex sibling.
  //
  // role="img" is load-bearing, not decoration. Tooltip puts its ariaLabel on
  // this element, and `aria-label` is PROHIBITED on an element with no role (it
  // maps to ARIA's `generic`), so a screen reader may drop it -- and a static
  // row is a plain <div>, with no enclosing button to compute a name from its
  // contents. Without the role the status of a shell row would be carried by
  // colour alone.
  const statusDot = (item: BackgroundTaskItem): JSX.Element => (
    <Tooltip text={backgroundTaskStatusLabel(item.status)} ariaLabel>
      <span
        class={`${styles.statusDot} ${statusDotClass(item.status)}`}
        data-testid="bg-task-status-dot"
        role="img"
      />
    </Tooltip>
  )

  const kindIcon = (item: BackgroundTaskItem): JSX.Element => {
    if (item.kind === 'shell')
      return <Terminal class={styles.taskIcon} size={14} />
    return <Bot class={styles.taskIcon} size={14} />
  }

  // The row's first line. Shared by the renderer and by the echo guard below,
  // so the guard compares against the string the row ACTUALLY shows: two copies
  // of this fallback chain could drift and silently disable the guard.
  const rowTitle = (item: BackgroundTaskItem): string =>
    item.title || item.description || item.rowKey

  // A shell row's title is the COMMAND it runs, so it is set as code. Every
  // other row's title is prose and hyphenates.
  const titleClass = (item: BackgroundTaskItem): string =>
    item.kind === 'shell'
      ? `${styles.taskTitle} ${styles.taskTitleCommand}`
      : styles.taskTitle

  // The row's second line. It must never repeat the first: a provider whose
  // spawn payload carries one string for both (Claude's local_bash gives the
  // command as the description, which is already the title) otherwise rendered
  // the same text twice. Neutral guard here rather than per provider, so the
  // next one to do it is covered too.
  //
  // Compared trimmed, because a provider that pads or wraps its copy produces a
  // string that is visibly the same echo but not `===` to the title.
  const secondary = (item: BackgroundTaskItem): string => {
    const text = isActiveBackgroundTaskStatus(item.status)
      ? item.activity || item.description || ''
      : backgroundTaskEndLabel(item.status)
    return text.trim() === rowTitle(item).trim() ? '' : text
  }

  // Explanatory tooltip for a final status whose bare label is ambiguous
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
      {kindIcon(item)}
      <div class={styles.taskBody}>
        {/* The dot comes FIRST in source order: a right float is placed against
            the top of the block it opens, so anything before it on that line
            would push it down to the next one. */}
        <div class={titleClass(item)}>
          {statusDot(item)}
          {rowTitle(item)}
        </div>
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
