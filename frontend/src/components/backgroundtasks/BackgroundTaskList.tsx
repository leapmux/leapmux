import type { Component, JSX } from 'solid-js'
import type { FilterTab } from '~/components/common/FilterTabBar'
import type { BackgroundTaskItem, BackgroundTaskKindFilter } from '~/stores/chatBackgroundTasks'
import Bot from 'lucide-solid/icons/bot'
import Terminal from 'lucide-solid/icons/terminal'
import { createMemo, createSignal, createUniqueId, For, Show } from 'solid-js'
import { ClippedText } from '~/components/common/ClippedText'
import { FilterTabBar } from '~/components/common/FilterTabBar'
import { StatusDot } from '~/components/common/StatusDot'
import { cleanName } from '~/lib/validate'
import {
  backgroundTaskEndLabel,
  backgroundTaskEndTooltip,
  backgroundTaskStatusLabel,
  filterBackgroundTasksByKind,
  groupBackgroundTasks,
  isActiveBackgroundTaskStatus,
  opensSubagentTranscript,
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

// The row's first line. Shared by the renderer and by the echo guard below, so
// the guard compares against the string the row ACTUALLY shows: two copies of
// this fallback chain could drift and silently disable the guard.
//
// Each arm is cleaned, and the fallback reads the CLEANED arm, so an arm that
// holds nothing a reader can see falls through to the next one instead of
// rendering as a blank line.
//
// The row key arm is why the clean is here. A row key is an IDENTITY, and the
// worker never REWRITES one: an unusable key is replaced whole by a digest of
// itself (bgtask.NormalizeRowKey), and every usable key reaches the browser
// byte for byte, because a rewrite would merge two provider keys into one
// registry row. Unreadable is not unusable -- at least one provider (Cursor)
// writes toolCallIds with an embedded newline, and those arrive verbatim. So
// cleaning at the READER is what lets the identity stay exact. `title` is
// already cleaned by the worker and `cleanName` is idempotent, so that arm
// passes through unchanged.
function rowTitle(item: BackgroundTaskItem): string {
  // `description` is optional, so the arm is guarded rather than passed as
  // `?? ''` -- an absent arm does no regex work at all.
  const arm = (text: string | undefined): string => (text ? cleanName(text) : '')
  // A fourth arm, because the third can clean to nothing as easily as the
  // first two: a row key that holds control characters only survives the
  // worker (ValidateRowKey refuses an unusable key rather than rewriting it,
  // and a bidirectional override IS usable as an identity) and reaches this
  // function as a non-empty string that `cleanName` empties. Every earlier arm
  // then falls through and the row draws a blank first line with a status dot
  // beside it. "Untitled" is what the workspace surfaces already show for a
  // title that resolves to nothing.
  return arm(item.title) || arm(item.description) || arm(item.rowKey) || 'Untitled'
}

// The group heading, cleaned for the reason `rowTitle` is.
//
// `groupKey`, the fallback, is a provider identity the worker drops rather than
// rewrites, so it arrives exactly as the provider wrote it -- and at least one
// provider (Cursor) writes identifiers with an embedded newline. Cleaning at
// the READER keeps the key verbatim for the lookup that groups by it.
//
// `groupLabel` is model-written -- Claude sends its workflow name -- and the
// worker now folds it with the title rule (`Upsert.Clean`), so this arm is
// idempotent on it rather than load-bearing. It stays because the ARM cannot
// tell which of the two it received, and because a heading is one line: a
// bidirectional override in a workflow name would otherwise reorder the text
// that sits above every row of that group.
function groupHeading(label: string): string {
  return cleanName(label)
}

// Code type only for a title the PROVIDER says is a verbatim command. Not every
// shell row has one: Claude sends `description || command`, so its title is the
// model's prose whenever it wrote any, and setting that in the monospace face
// reads worse than setting a command in the normal one.
function titleClass(item: BackgroundTaskItem): string {
  return item.titleIsCommand
    ? `${styles.taskTitle} ${styles.taskTitleCommand}`
    : styles.taskTitle
}

// The row's second line. It must never repeat the first: a provider whose spawn
// payload carries one string for both (Claude's local_bash gives the command as
// the description, which is already the title) otherwise rendered the same text
// twice. Neutral guard here rather than per provider, so the next one to do it
// is covered too.
//
// `activity` and `description` arrive from the provider UNCLEANED -- the worker
// cleans `title` alone (bgtask.Upsert.CleanTitle) -- so this arm cleans them for
// the same reason `rowTitle` does, and a bidirectional override in an activity
// string can no longer reorder the line.
//
// Both sides of the echo test are cleaned. Comparing a cleaned title against a
// raw copy defeats the guard for every string the fold rewrites: `npm test  -x`
// folds its double space and stops matching the title it IS. Trimming alone
// cannot see an interior run. `cleanName` is idempotent, so cleaning the title
// arm again costs nothing and keeps the two sides symmetric.
//
// The caller passes the title it already computed, so the row cleans once.
function secondary(item: BackgroundTaskItem, title: string): string {
  const raw = isActiveBackgroundTaskStatus(item.status)
    ? item.activity || item.description || ''
    : backgroundTaskEndLabel(item.status)
  const text = raw ? cleanName(raw) : ''
  return text.trim() === title.trim() ? '' : text
}

// Explanatory tooltip for a final status whose bare label is ambiguous
// (e.g. "Interrupted" really means the worker/agent process restarted).
function secondaryTooltip(item: BackgroundTaskItem): string | undefined {
  if (isActiveBackgroundTaskStatus(item.status))
    return undefined
  return backgroundTaskEndTooltip(item.status)
}

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
 * at the end of the title line, and a secondary line. Each line is held to one
 * line and clipped, and gives its full text on hover. Subagent rows with a
 * childAgentId are clickable buttons; shell rows are static.
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
  // The exact state stays available as the dot's accessible name and tooltip,
  // and as the row's `data-status`, which is what tests and E2E select on.
  //
  // Rendered at the right end of the title line, as a flex sibling of the
  // title -- see titleRow in the stylesheet.
  const renderStatusDot = (item: BackgroundTaskItem): JSX.Element => (
    <StatusDot
      class={statusDotClass(item.status)}
      label={backgroundTaskStatusLabel(item.status)}
      tooltip
      testId="bg-task-status-dot"
    />
  )

  const kindIcon = (item: BackgroundTaskItem): JSX.Element => {
    if (item.kind === 'shell')
      return <Terminal class={styles.taskIcon} size={14} />
    return <Bot class={styles.taskIcon} size={14} />
  }

  // The line is clipped to one line, so ClippedText offers the full string on
  // hover. An explanatory tip is passed as the DETAIL, so it stands under the
  // label rather than in its place -- a clipped label keeps its route back even
  // when the row also has an explanation. A detail also shows while the label
  // fits, because it carries what the label cannot.
  const renderSecondary = (item: BackgroundTaskItem, title: string): JSX.Element => {
    const text = secondary(item, title)
    if (!text)
      return null
    return (
      <ClippedText
        text={text}
        class={styles.taskSecondary}
        detail={secondaryTooltip(item)}
      />
    )
  }

  // The row's inner content and its data attributes are identical for both
  // element kinds; only the tag and the click handler differ. Building them once
  // keeps the clickable and static rows from drifting apart.
  const rowBody = (item: BackgroundTaskItem): JSX.Element => {
    // Cleaned once per row: the title line renders it and the echo guard in
    // `secondary` compares against it.
    const title = rowTitle(item)
    return (
      <>
        {kindIcon(item)}
        <div class={styles.taskBody}>
          <div class={styles.titleRow}>
            <ClippedText text={title} class={titleClass(item)} />
            {renderStatusDot(item)}
          </div>
          {renderSecondary(item, title)}
        </div>
      </>
    )
  }

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
    const clickable = opensSubagentTranscript(item) && !!props.onOpenSubagent
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
                <ClippedText text={groupHeading(group.label)} class={styles.groupHeader} />
                <For each={group.items}>{item => renderRow(item)}</For>
              </>
            )}
          </For>
        </Show>
      </div>
    </div>
  )
}
