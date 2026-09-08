import type { Component } from 'solid-js'
import type { FilterTab } from '~/components/common/FilterTabBar'
import type { BackgroundTaskItem, BackgroundTaskKindFilter } from '~/stores/chatBackgroundTasks'
import { createSignal, createUniqueId } from 'solid-js'
import { FilterTabBar } from '~/components/common/FilterTabBar'
import { BackgroundTaskList } from './BackgroundTaskList'
import * as styles from './BackgroundTaskPanel.css'

/**
 * Every tab: its label, and what the region says when it holds no rows.
 *
 * A `Record` over the kind union, so a new `BackgroundTaskItem['kind']` fails
 * to compile until it has both. A plain array type-checked with any subset,
 * which let a new kind ship reachable only through All -- the tab list and the
 * empty messages have to be one declaration for that to be impossible.
 *
 * A new kind still needs two things this cannot force: a case in
 * `protoBackgroundTaskToStore`, and a case in the kind-icon `Show` inside
 * `./BackgroundTaskList.tsx`.
 */
const LIST_TABS_META: Record<BackgroundTaskKindFilter, { label: string, empty: string }> = {
  all: { label: 'All', empty: 'No background tasks' },
  subagent: { label: 'Subagents', empty: 'No subagents' },
  shell: { label: 'Shell', empty: 'No shell commands' },
}

// FilterTabBar reconciles this list by reference. Keep one module-level list so
// a task update does not replace the tab buttons and remove keyboard focus.
const LIST_TABS: readonly FilterTab<BackgroundTaskKindFilter>[] = (
  Object.keys(LIST_TABS_META) as BackgroundTaskKindFilter[]
).map(key => ({ key, label: LIST_TABS_META[key].label }))

export interface BackgroundTaskPanelProps {
  tasks: BackgroundTaskItem[]
  /** The worker could not load the registry. */
  loadFailed?: boolean
  onOpenSubagent?: (item: BackgroundTaskItem) => void
  /** The host surface controls the panel size. */
  variant: 'sidebar' | 'popover'
}

/**
 * BackgroundTaskPanel is an agent's background-task registry: its subagents and
 * its shell commands, behind one filter per kind.
 *
 * Shared by the sidebar section and the ThinkingIndicator popover. It owns the
 * tab bar and the scrolling region; `BackgroundTaskList` renders the rows. The
 * session goal is NOT here -- `~/components/todo/GoalsAndTodos` renders it
 * above the to-do list, in the section named for both.
 */
export const BackgroundTaskPanel: Component<BackgroundTaskPanelProps> = (props) => {
  const [tab, setTab] = createSignal<BackgroundTaskKindFilter>('all')
  const panelId = createUniqueId()

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
        tabs={LIST_TABS}
        active={tab()}
        onSelect={setTab}
        ariaLabel="Filter background tasks"
        panelId={panelId}
        testId="bg-task-filter-tab-bar"
        tabTestId={key => `bg-task-filter-${key}`}
      />
      {/* The region is the scroller and can contain no focusable row. Its tab
          stop lets a keyboard user scroll a long registry. */}
      <div id={panelId} role="tabpanel" tabIndex={0} class={styles.rows}>
        <BackgroundTaskList
          tasks={props.tasks}
          kind={tab()}
          emptyMessage={LIST_TABS_META[tab()].empty}
          loadFailed={props.loadFailed}
          onOpenSubagent={props.onOpenSubagent}
        />
      </div>
    </div>
  )
}
