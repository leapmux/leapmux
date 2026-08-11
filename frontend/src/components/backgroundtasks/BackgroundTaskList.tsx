import type { Component, JSX } from 'solid-js'
import type { FilterTab } from '~/components/common/FilterTabBar'
import type { BackgroundTaskItem, BackgroundTaskKindFilter } from '~/stores/chatBackgroundTasks'
import Bot from 'lucide-solid/icons/bot'
import Terminal from 'lucide-solid/icons/terminal'
import { createMemo, createSignal, createUniqueId, For, Show } from 'solid-js'
import { FilterTabBar } from '~/components/common/FilterTabBar'
import { Tooltip } from '~/components/common/Tooltip'
import {
  backgroundTaskEndLabel,
  backgroundTaskEndTooltip,
  backgroundTaskStatusLabel,
  filterBackgroundTasksByKind,
  groupBackgroundTasks,
  isActiveBackgroundTaskStatus,
  sortBackgroundTasks,
} from '~/stores/chatBackgroundTasks'
import * as styles from './BackgroundTaskList.css'

interface BackgroundTaskListProps {
  tasks: BackgroundTaskItem[]
  /**
   * The worker could not answer for this registry, so an empty list means "no
   * answer", not "no tasks". Says so in place of the empty message: the two are
   * otherwise indistinguishable, and the section is hidden when it is empty, so
   * a failure that reads as emptiness disappears entirely.
   */
  loadFailed?: boolean
  onOpenSubagent?: (item: BackgroundTaskItem) => void
  /**
   * Which surface hosts the list, which is what decides how the root is sized.
   * `sidebar` fills the section's content box; `popover` caps its own height and
   * width, because the DropdownMenu card sizes to whatever it holds. The rows
   * scroll either way, so the kind tabs stay on screen.
   */
  variant: 'sidebar' | 'popover'
}

/**
 * Every kind tab: its label, and what it says when it holds no rows.
 *
 * A `Record` over the filter union, so a new `BackgroundTaskItem['kind']` fails
 * to compile until it has both. A plain array of tabs type-checked with any
 * subset, which let a new kind ship reachable only through All -- the tab list
 * and the empty messages have to be one declaration for that to be impossible.
 *
 * A new kind still needs two things this cannot force: an arm in
 * `protoBackgroundTaskToStore`, and an icon in `kindIcon` below.
 */
const KIND_TABS_META: Record<BackgroundTaskKindFilter, { label: string, empty: string }> = {
  all: { label: 'All', empty: 'No background tasks' },
  subagent: { label: 'Subagents', empty: 'No subagents' },
  shell: { label: 'Shell', empty: 'No shell commands' },
}

/** The kind tabs, in render order -- the key order of {@link KIND_TABS_META}. */
const KIND_TABS: readonly FilterTab<BackgroundTaskKindFilter>[]
  = (Object.keys(KIND_TABS_META) as BackgroundTaskKindFilter[])
    .map(key => ({ key, label: KIND_TABS_META[key].label }))

/**
 * What an unanswerable registry says instead.
 *
 * Identifies the worker, because that is where the fault is and where the log
 * line with the reason is. "No background tasks" here would be a lie that also
 * hides its own section.
 */
const LOAD_FAILED_MESSAGE = 'Could not load background tasks from the worker'

/** The status palette: queued, in progress, succeeded, failed. */
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
    // A queued task is drawn as a hollow ring, not a filled dot. Running is the
    // only state that pulses, and the pulse is suppressed under reduced motion --
    // so sharing one filled dot made a queued task and a running one identical
    // for exactly the readers who cannot use the animation.
    case 'pending':
      return styles.statusDotPending
    default:
      return styles.statusDotActive
  }
}

/**
 * BackgroundTaskList renders the background-task registry for the sidebar
 * section and the ThinkingIndicator popover. Shared by both surfaces. Filters
 * by kind through its own tab bar, sorts active-first (running before pending),
 * groups by workflow/phase, and renders a kind icon, a title with its status dot
 * floated to the end of the first line, and a secondary line. Subagent rows with
 * a childAgentId are clickable buttons; shell rows are static.
 */
export const BackgroundTaskList: Component<BackgroundTaskListProps> = (props) => {
  // Per mount, not shared: the sidebar section and the popover are separate
  // mounts, and a filter one of them set is not a preference for the other.
  const [kindFilter, setKindFilter] = createSignal<BackgroundTaskKindFilter>('all')
  // Ties each role=tab to the region it swaps. Unique per mount, because both
  // surfaces can be on screen at once and an id may name only one element.
  const panelId = createUniqueId()

  const visible = createMemo(() => filterBackgroundTasksByKind(props.tasks, kindFilter()))

  /**
   * Whether the empty slot reports a FAILURE rather than an absence.
   *
   * Keyed on the whole registry, not on the selected tab. A failed load leaves
   * the rows it already had in place, so a root can hold subagent rows AND the
   * failure flag -- and on the Shell tab, which legitimately has none, the
   * failure message would then stand over a registry the same mount is showing
   * two clicks away. The message stands in for MISSING content, never over it.
   */
  const reportsLoadFailure = () => !!props.loadFailed && props.tasks.length === 0
  // Memoized so a broadcast tick re-runs sort+group once (not twice, once per
  // JSX read of `.ungrouped`/`.groups`), and only when the visible rows change.
  const grouped = createMemo(() => groupBackgroundTasks(sortBackgroundTasks(visible())))

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

  // Code type only for a title the PROVIDER says is a verbatim command. Not
  // every shell row has one: Claude sends `description || command`, so its
  // title is the model's prose whenever it wrote any, and setting that in the
  // monospace face reads worse than setting a command in the normal one.
  const titleClass = (item: BackgroundTaskItem): string =>
    item.titleIsCommand
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
    <div
      class={styles.root}
      classList={{
        [styles.sidebarRoot]: props.variant === 'sidebar',
        [styles.popoverRoot]: props.variant === 'popover',
      }}
      data-testid="bg-task-list"
    >
      <FilterTabBar
        tabs={KIND_TABS}
        active={kindFilter()}
        onSelect={setKindFilter}
        ariaLabel="Filter background tasks"
        panelId={panelId}
        testId="bg-task-filter-tab-bar"
        tabTestId={key => `bg-task-filter-${key}`}
      />
      {/* `tabIndex`, because `rows` is the scroller for both surfaces and holds
          nothing focusable of its own: the rows are buttons only when a
          subagent can be opened. Without it a keyboard user cannot reach a
          registry taller than the box -- the arrow keys land on the tablist,
          which spends them switching kind tabs. The Files section's panel
          carries the same attribute for the same reason. */}
      <div id={panelId} role="tabpanel" tabIndex={0} class={styles.rows}>
        <Show
          when={visible().length > 0}
          fallback={(
            <div
              class={styles.emptyMessage}
              classList={{ [styles.loadFailedMessage]: reportsLoadFailure() }}
              data-testid={reportsLoadFailure() ? 'bg-task-load-failed' : 'bg-task-empty'}
            >
              {reportsLoadFailure() ? LOAD_FAILED_MESSAGE : KIND_TABS_META[kindFilter()].empty}
            </div>
          )}
        >
          <For each={grouped().ungrouped}>{item => renderRow(item)}</For>
          <For each={grouped().groups}>
            {group => (
              <>
                <div class={styles.groupHeader}>{group.label}</div>
                <For each={group.items}>{item => renderRow(item)}</For>
              </>
            )}
          </For>
        </Show>
      </div>
    </div>
  )
}
