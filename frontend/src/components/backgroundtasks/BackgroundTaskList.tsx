import type { Component, JSX } from 'solid-js'
import type { BackgroundTaskItem, BackgroundTaskKindFilter } from '~/stores/chatBackgroundTasks'
import Bot from 'lucide-solid/icons/bot'
import Terminal from 'lucide-solid/icons/terminal'
import { createMemo, For, Show } from 'solid-js'
import { ClippedText } from '~/components/common/ClippedText'
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
  /** Which kind the host's selected tab shows. */
  kind: BackgroundTaskKindFilter
  /** What to say when this kind has no rows. Supplied by the host, which owns the tabs. */
  emptyMessage: string
  /**
   * The worker could not answer for this registry, so an empty list means "no
   * answer", not "no tasks". Says so in place of the empty message: the two are
   * otherwise indistinguishable, and the section is hidden when it is empty, so
   * a failure that reads as emptiness disappears entirely.
   */
  loadFailed?: boolean
  onOpenSubagent?: (item: BackgroundTaskItem) => void
}

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
 * BackgroundTaskList renders the ROWS of the background-task registry for the
 * kind its host selected. It sorts active-first (running before pending),
 * groups by workflow/phase, and renders a kind icon, a title with its status dot
 * at the end of the title line, and a secondary line. Each line is held to one
 * line and clipped, and gives its full text on hover. Subagent rows with a
 * childAgentId are clickable buttons; shell rows are static.
 *
 * It owns no tab bar and no root box: AgentWorkPanel does, because the panel
 * also shows the session goal and one host has to decide what a tab contains.
 * Everything here exists to keep a ROW's identity stable across a broadcast,
 * which is why it stayed one component when the shell moved out.
 */
export const BackgroundTaskList: Component<BackgroundTaskListProps> = (props) => {
  const visible = createMemo(() => filterBackgroundTasksByKind(props.tasks, props.kind))

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
  // The keys alone, so the group `For` below reconciles on a primitive that
  // survives the memo rebuilding its group objects. Its own memo, so a recompute
  // that leaves the set of groups unchanged does not re-run the `For` at all.
  const groupKeys = createMemo(
    () => grouped().groups.map(g => g.key),
    undefined,
    { equals: (a, b) => a.length === b.length && a.every((k, i) => k === b[i]) },
  )

  /**
   * The row's inner content and its data attributes are identical for both
   * element kinds; only the tag and the click handler differ. Building them once
   * keeps the clickable and static rows from drifting apart.
   *
   * Every part of it is a reactive EXPRESSION on one field, never a helper that
   * returns an element. That distinction is the whole point of this shape. A
   * `{renderStatusDot(item)}` re-runs its whole body whenever any field it reads
   * changes and puts a NEW element in place of the old one, so a task that
   * reported progress lost the tooltip under the pointer and restarted the pulse
   * on its status dot. A prop getter updates one attribute of an element that
   * stays.
   *
   * `~/stores/chatPerAgentStore`'s `setReconciled` is the other half: without a
   * store that keeps a row's identity across a broadcast, `<For>` would rebuild
   * the row before any of this could matter.
   */
  const rowBody = (item: BackgroundTaskItem): JSX.Element => {
    // A memo, because two bindings read it -- the title line, and the echo guard
    // in `secondary` -- and a row must not clean its title twice per update or
    // let the two reads disagree.
    const title = createMemo(() => rowTitle(item))
    const secondaryText = createMemo(() => secondary(item, title()))
    return (
      <>
        {/* `Show`, not a function that returns one icon or the other: a kind
            that changed would otherwise replace the element rather than swap
            the branch. */}
        <Show
          when={item.kind === 'shell'}
          fallback={<Bot class={styles.taskIcon} size={14} />}
        >
          <Terminal class={styles.taskIcon} size={14} />
        </Show>
        <div class={styles.taskBody}>
          <div class={styles.titleRow}>
            <ClippedText text={title()} class={titleClass(item)} />
            {/* Status reads as COLOR on one constant dot, not as a different
                glyph per state. Six shapes made the column a legend to
                memorize; one dot in the status palette (in progress /
                succeeded / failed) is legible at a glance, and an in-progress
                dot pulses so activity is visible without a spinner. The exact
                state stays available as the dot's accessible name and tooltip,
                and as the row's `data-status`, which is what tests and E2E
                select on.

                At the right end of the title line, as a flex sibling of the
                title -- see titleRow in the stylesheet. */}
            <StatusDot
              class={statusDotClass(item.status)}
              label={backgroundTaskStatusLabel(item.status)}
              tooltip
              testId="bg-task-status-dot"
            />
          </div>
          {/* The line is clipped to one line, so ClippedText offers the full
              string on hover. An explanatory tip is passed as the DETAIL, so it
              stands under the label rather than in its place -- a clipped label
              keeps its route back even when the row also has an explanation. A
              detail also shows while the label fits, because it carries what
              the label cannot.

              `Show` compares its condition by TRUTHINESS, so an activity string
              that changes from one non-empty value to another updates the text
              in place and never rebuilds the label. */}
          <Show when={secondaryText()}>
            <ClippedText
              text={secondaryText()}
              class={styles.taskSecondary}
              detail={secondaryTooltip(item)}
            />
          </Show>
        </div>
      </>
    )
  }

  // `extraClass` carries taskRowStatic for the non-clickable row, which drops
  // the pointer cursor taskRow sets for the clickable one.
  //
  // GETTERS, because Solid spreads this object inside a render effect and reads
  // each property there: a plain value is read once and then never again. Every
  // field that can change while the row lives is one -- the status a task
  // finishes with, and the child agent id a subagent gets only once it spawns.
  // A frozen `data-status` would be invisible on screen and wrong for every E2E
  // locator that selects on it.
  const rowAttrs = (item: BackgroundTaskItem, clickable?: () => boolean) => ({
    'class': styles.taskRow,
    get 'classList'() {
      return {
        [styles.taskStruck]: !isActiveBackgroundTaskStatus(item.status),
        // taskRowStatic drops the pointer cursor taskRow sets. A getter, because
        // a subagent row becomes clickable mid-life -- see renderRow.
        [styles.taskRowStatic]: clickable ? !clickable() : true,
      }
    },
    'data-testid': 'bg-task-row',
    get 'data-status'() { return item.status },
    get 'data-kind'() { return item.kind },
    get 'data-child-agent-id'() { return item.childAgentId ?? '' },
  })

  const renderRow = (item: BackgroundTaskItem): JSX.Element => {
    // The TAG is decided by fields that never change over a row's life, so the
    // element survives every broadcast. `childAgentId` is not one of them: the
    // worker reports it a broadcast or two after the row itself, and a `Show`
    // keyed on it swapped a <div> for a <button> at exactly the moment the user
    // is watching that row -- rebuilding its whole body, closing the title
    // tooltip under the pointer and restarting the status dot's pulse. That is
    // the flicker the rest of this component exists to remove.
    //
    // So a subagent row that the caller can open is ALWAYS a <button>, and the
    // arrival of the id only flips one attribute on it.
    const openable = item.kind === 'subagent' && !!props.onOpenSubagent
    const clickable = () => opensSubagentTranscript(item) && !!props.onOpenSubagent
    if (!openable)
      return <div {...rowAttrs(item)}>{rowBody(item)}</div>
    return (
      <button
        type="button"
        {...rowAttrs(item, clickable)}
        // aria-disabled, never the `disabled` attribute: a disabled control
        // dispatches no pointer event of its own OR to its descendants, which
        // would kill the row's own title tooltip for as long as the subagent is
        // still spawning.
        aria-disabled={clickable() ? undefined : 'true'}
        onClick={() => clickable() && props.onOpenSubagent?.(item)}
      >
        {rowBody(item)}
      </button>
    )
  }

  return (
    <Show
      when={visible().length > 0}
      fallback={(
        <div
          class={styles.emptyState}
          classList={{ [styles.emptyStateFailed]: reportsLoadFailure() }}
          data-testid={reportsLoadFailure() ? 'bg-task-load-failed' : 'bg-task-empty'}
        >
          {reportsLoadFailure() ? LOAD_FAILED_MESSAGE : props.emptyMessage}
        </div>
      )}
    >
      <For each={grouped().ungrouped}>{item => renderRow(item)}</For>
      {/* Keyed by the group KEY, not by the group object. `groupBackgroundTasks`
          builds fresh `{key, label, items}` objects on every run and `For`
          reconciles by reference, so iterating the objects tore down and
          rebuilt every grouped row whenever the memo re-ran -- which any
          status change does, because the sort reads `status`. That is the
          same flicker `setReconciled` removes for the ungrouped rows, and it
          reached every row of a Claude workflow, which groups its subagents.
          `For` compares primitives by value, and a group key is unique by
          construction. */}
      <For each={groupKeys()}>
        {(key) => {
          const group = createMemo(() => grouped().groups.find(g => g.key === key))
          return (
            <>
              <ClippedText text={groupHeading(group()?.label ?? key)} class={styles.groupHeader} />
              <For each={group()?.items ?? []}>{item => renderRow(item)}</For>
            </>
          )
        }}
      </For>
    </Show>
  )
}
