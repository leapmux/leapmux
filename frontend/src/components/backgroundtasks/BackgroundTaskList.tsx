import type { Component, JSX } from 'solid-js'
import type { BackgroundTaskItem, BackgroundTaskKindFilter } from '~/stores/chatBackgroundTasks'
import Bot from 'lucide-solid/icons/bot'
import CircleHelp from 'lucide-solid/icons/circle-help'
import Terminal from 'lucide-solid/icons/terminal'
import WorkflowIcon from 'lucide-solid/icons/workflow'
import { createMemo, For, Match, Show, Switch } from 'solid-js'
import { ClippedText } from '~/components/common/ClippedText'
import { StatusDot } from '~/components/common/StatusDot'
import { BACKGROUND_TASK_STATUS_TOKEN } from '~/generated/contracts/worker-vocab'
import { cleanName } from '~/lib/validate'
import {
  backgroundTaskEndLabel,
  backgroundTaskEndTooltip,
  backgroundTaskStatusLabel,
  filterBackgroundTasksByKind,
  groupBackgroundTasks,
  isOpenBackgroundTaskStatus,
  opensSubagentTranscript,
  sortBackgroundTasks,
} from '~/stores/chatBackgroundTasks'
import * as statusDotStyles from '~/styles/statusDot.css'
import * as styles from './BackgroundTaskList.css'

interface BackgroundTaskListProps {
  tasks: BackgroundTaskItem[]
  /** Which kind the host's selected tab shows. */
  kind: BackgroundTaskKindFilter
  /** The host supplies the empty message for its selected kind. */
  emptyMessage: string
  /**
   * The Worker could not read this registry. An empty list therefore proves no task count.
   * Show the failure message because an empty registry normally hides the section.
   */
  loadFailed?: boolean
  onOpenSubagent?: (item: BackgroundTaskItem) => void
}

/**
 * Identify the Worker when its registry read fails.
 * Its log contains the cause. An empty-task message would hide this failure and its section.
 */
const LOAD_FAILED_MESSAGE = 'Could not load background tasks from the worker'

// The renderer and repeat-text guard share the same first line.
// Clean each candidate before the fallback selects it. Invisible text cannot create a blank label.
// Native row keys retain exact identity. NormalizeRowKey replaces invalid keys with a digest rather than editing them.
// Cursor keys can contain newlines, so the browser cleans display text without changing the key.
// The Worker already cleans titles. cleanName preserves that cleaned value.
function rowTitle(item: BackgroundTaskItem): string {
  // Optional descriptions skip the cleaner when absent.
  const readable = (text: string | undefined): string => (text ? cleanName(text) : '')
  // A valid identity can contain only invisible bidirectional characters.
  // Use the workspace's Untitled label when every cleaned candidate is empty.
  return readable(item.title) || readable(item.description) || readable(item.rowKey) || 'Untitled'
}

// Clean the group heading without changing the key that groups its rows.
// The fallback key can contain a newline. The label can contain bidirectional controls.
// Upsert.Clean already cleans group labels, but this function also accepts an exact native key.
function groupHeading(label: string): string {
  return cleanName(label)
}

// Use monospace only when provider metadata identifies a verbatim command.
// Claude shell titles prefer a description over the command, so the shell kind alone cannot decide.
function titleClass(item: BackgroundTaskItem): string {
  return item.titleIsCommand
    ? `${styles.taskTitle} ${styles.taskTitleCommand}`
    : styles.taskTitle
}

// The second line must not repeat the first. Claude can supply the same command for both lines.
// Clean raw activity and description text before comparison, including bidirectional controls and repeated interior spaces.
// Compare that value with the caller's already-cleaned title. Trimming alone cannot normalize interior spaces.
function secondary(item: BackgroundTaskItem, title: string): string {
  const raw = isOpenBackgroundTaskStatus(item.status)
    ? item.activity || item.description || ''
    : backgroundTaskEndLabel(item.status)
  const text = raw ? cleanName(raw) : ''
  return text.trim() === title.trim() ? '' : text
}

// Explain a final status whose label omits the cause, such as an interrupted process after a restart.
// Return an empty string for an absent detail because ClippedText uses that convention.
function secondaryTooltip(item: BackgroundTaskItem): string {
  if (isOpenBackgroundTaskStatus(item.status))
    return ''
  return backgroundTaskEndTooltip(item.status) ?? ''
}

/** Select the status color and shape. */
function statusDotClass(status: BackgroundTaskItem['status']): string {
  switch (status) {
    case BACKGROUND_TASK_STATUS_TOKEN.Succeeded:
      return statusDotStyles.statusDotSuccess
    // A crash interrupts unfinished work. An explicit user stop stays muted.
    case BACKGROUND_TASK_STATUS_TOKEN.Failed:
    case BACKGROUND_TASK_STATUS_TOKEN.Interrupted:
      return statusDotStyles.statusDotDanger
    case BACKGROUND_TASK_STATUS_TOKEN.Stopped:
    case BACKGROUND_TASK_STATUS_TOKEN.Paused:
    case BACKGROUND_TASK_STATUS_TOKEN.EndedWithUnknownOutcome:
      return statusDotStyles.statusDotMuted
    // A queued task uses a hollow ring. Only a running task pulses.
    // Reduced motion removes the pulse, so shape must still distinguish the two states.
    case BACKGROUND_TASK_STATUS_TOKEN.Pending:
      return statusDotStyles.statusDotPending
    default:
      return statusDotStyles.statusDotActive
  }
}

/**
 * BackgroundTaskList renders registry rows for the selected kind.
 * It sorts active rows before paused rows and groups workflow phases.
 * Each row contains a kind icon, a title, a status dot, and an optional second line.
 * Each clipped line exposes its full text on hover.
 * Subagent rows with a childAgentId open transcripts. Shell and workflow rows remain static.
 * BackgroundTaskPanel owns the tabs and root. This component preserves row identity across broadcasts.
 */
export const BackgroundTaskList: Component<BackgroundTaskListProps> = (props) => {
  const visible = createMemo(() => filterBackgroundTasksByKind(props.tasks, props.kind))

  /**
   * Show a load failure only when the whole registry contains no retained rows.
   * A failed refresh preserves earlier rows. An empty selected kind must not make that retained registry appear unreadable.
   */
  const reportsLoadFailure = () => !!props.loadFailed && props.tasks.length === 0
  // Sort and group once per visible-row change, even when JSX reads both collections.
  const grouped = createMemo(() => groupBackgroundTasks(sortBackgroundTasks(visible())))
  // Primitive keys preserve group identity when the grouping memo creates new objects.
  // Equal key arrays prevent an unchanged group list from rerunning For.
  const groupKeys = createMemo(
    () => grouped().groups.map(g => g.key),
    undefined,
    { equals: (a, b) => a.length === b.length && a.every((k, i) => k === b[i]) },
  )

  /**
   * Static and clickable rows share their content and attributes. Only their tag and click handler differ.
   * Reactive expressions update individual fields without replacing the element.
   * Replacing the status element would close its tooltip and restart its pulse after each progress update.
   * The store's setReconciled also preserves row identity, so For retains these elements across broadcasts.
   */
  const rowBody = (item: BackgroundTaskItem): JSX.Element => {
    // The title and repeat-text guard share one cleaned value per update.
    const title = createMemo(() => rowTitle(item))
    const secondaryText = createMemo(() => secondary(item, title()))
    return (
      <>
        {/* Switch updates the selected icon branch when the kind changes. */}
        <Switch>
          <Match when={item.kind === 'shell'}>
            <Terminal class={styles.taskIcon} size={14} />
          </Match>
          <Match when={item.kind === 'workflow'}>
            <WorkflowIcon class={styles.taskIcon} size={14} />
          </Match>
          <Match when={item.kind === 'subagent'}>
            <Bot class={styles.taskIcon} size={14} />
          </Match>
          <Match when={item.kind === 'unknown'}>
            <CircleHelp class={styles.taskIcon} size={14} />
          </Match>
        </Switch>
        <div class={styles.taskBody}>
          <div class={styles.titleRow}>
            <ClippedText text={title()} class={titleClass(item)} testId="bg-task-title" />
            {/* Keep one status element at the title line's right edge.
                Color and shape identify status. Running activity adds a pulse without a separate spinner.
                The accessible name, tooltip, and data-status retain the exact state. */}
            <StatusDot
              class={statusDotClass(item.status)}
              label={backgroundTaskStatusLabel(item.status)}
              tooltip
              testId="bg-task-status-dot"
            />
          </div>
          {/* ClippedText exposes the complete second line on hover.
              A detail appears below the label and also appears when the label fits.
              Show preserves the element when one nonempty activity changes to another. */}
          <Show when={secondaryText()}>
            <ClippedText
              text={secondaryText()}
              class={styles.taskSecondary}
              testId="bg-task-secondary"
              detail={secondaryTooltip(item)}
            />
          </Show>
        </div>
      </>
    )
  }

  // Attribute getters let Solid update a retained row after its fields change.
  // This includes exact task identity, status, and a child ID that arrives after the row.
  // taskRowStatic removes the pointer cursor while the row cannot open a transcript.
  const rowAttrs = (item: BackgroundTaskItem, clickable?: () => boolean) => ({
    'class': styles.taskRow,
    get 'classList'() {
      return {
        [styles.taskStruck]: !isOpenBackgroundTaskStatus(item.status),
        // Recompute the cursor class when the child transcript becomes available.
        [styles.taskRowStatic]: clickable ? !clickable() : true,
      }
    },
    'data-testid': 'bg-task-row',
    get 'data-task-id'() { return item.rowKey },
    get 'data-status'() { return item.status },
    get 'data-kind'() { return item.kind },
    get 'data-child-agent-id'() { return item.childAgentId ?? '' },
  })

  const renderRow = (item: BackgroundTaskItem): JSX.Element => {
    // Stable row kind and host capability decide the element tag.
    // A delayed childAgentId updates clickability without replacing the row or its tooltip and status dot.
    const openable = item.kind === 'subagent' && !!props.onOpenSubagent
    const clickable = () => opensSubagentTranscript(item) && !!props.onOpenSubagent
    if (!openable)
      return <div {...rowAttrs(item)}>{rowBody(item)}</div>
    return (
      <button
        type="button"
        {...rowAttrs(item, clickable)}
        // aria-disabled preserves pointer events for the title tooltip while the child starts.
        // The click handler still refuses a row without an available transcript.
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
      {/* For compares primitive group keys by value.
          Group objects change after each status update, so their reference identity cannot preserve grouped rows.
          Stable unique keys retain row tooltips and status animations across those updates. */}
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
