import type { Component } from 'solid-js'
import type { FilterTab } from '~/components/common/FilterTabBar'
import type { BackgroundTaskItem, BackgroundTaskKindFilter } from '~/stores/chatBackgroundTasks'
import type { GoalAction, GoalProgress, SessionGoal } from '~/stores/chatGoal'
import { createSignal, createUniqueId, Show } from 'solid-js'
import { FilterTabBar } from '~/components/common/FilterTabBar'
import * as styles from './AgentWorkPanel.css'
import { BackgroundTaskList } from './BackgroundTaskList'
import { GoalCard } from './GoalCard'

/**
 * The panel's tab keys: the background-task kinds, plus the session goal.
 *
 * `goal` is NOT a `BackgroundTaskKind`, and the distinction is mechanical
 * rather than tidy. `BackgroundTaskKindFilter` is derived from
 * `BackgroundTaskItem['kind']`, so a goal enrolled there would flow into
 * `countActiveBackgroundTasks` -> `rootWorkState` -> `shouldShowThinkingIndicator`
 * and keep the compass spinning and Interrupt armed for the goal's whole life.
 * The panel therefore owns its own key union and hands the registry only the
 * three kinds it understands.
 */
export type AgentWorkTabKey = BackgroundTaskKindFilter | 'goal'

/**
 * Every tab: its label, and what the task region says when it holds no rows.
 *
 * A `Record` over the key union, so a new `BackgroundTaskItem['kind']` fails to
 * compile until it has both. A plain array type-checked with any subset, which
 * let a new kind ship reachable only through All -- the tab list and the empty
 * messages have to be one declaration for that to be impossible.
 *
 * A new kind still needs two things this cannot force: a case in
 * `protoBackgroundTaskToStore`, and a case in the kind-icon `Show` inside
 * `./BackgroundTaskList.tsx`.
 */
const TABS_META: Record<AgentWorkTabKey, { label: string, empty: string }> = {
  all: { label: 'All', empty: 'No background tasks' },
  subagent: { label: 'Subagents', empty: 'No subagents' },
  shell: { label: 'Shell', empty: 'No shell commands' },
  goal: { label: 'Goal', empty: 'No session goal' },
}

/** The tabs, in render order -- the key order of {@link TABS_META}. */
const TABS: readonly FilterTab<AgentWorkTabKey>[]
  = (Object.keys(TABS_META) as AgentWorkTabKey[])
    .map(key => ({ key, label: TABS_META[key].label }))

/**
 * Which registry kinds a tab shows, or undefined for a tab that shows none.
 *
 * This is the one place the panel's key union narrows to the registry's, so
 * `filterBackgroundTasksByKind` is never called with a key it does not know.
 */
function taskKindFor(tab: AgentWorkTabKey): BackgroundTaskKindFilter | undefined {
  return tab === 'goal' ? undefined : tab
}

export interface AgentWorkPanelProps {
  tasks: BackgroundTaskItem[]
  goal?: SessionGoal
  goalProgress: GoalProgress
  /** What the running agent can do; see GoalCardProps.supportedActions. */
  goalActions: GoalAction[]
  /**
   * The worker could not answer for this registry, so an empty list means "no
   * answer", not "no tasks". Says so in place of the empty message: the two are
   * otherwise indistinguishable, and the section is hidden when it is empty, so
   * a failure that reads as emptiness disappears entirely.
   */
  loadFailed?: boolean
  onOpenSubagent?: (item: BackgroundTaskItem) => void
  onGoalAction?: (action: GoalAction) => void
  /**
   * Which surface hosts the panel, which is what decides how the root is sized.
   * `sidebar` fills the section's content box; `popover` caps its own height and
   * width, because the DropdownMenu card sizes to whatever it holds. The rows
   * scroll either way, so the tabs stay on screen.
   */
  variant: 'sidebar' | 'popover'
}

/**
 * AgentWorkPanel is what an agent is working on: its session goal, and its
 * background-task registry.
 *
 * Shared by the sidebar section and the ThinkingIndicator popover. It owns the
 * tab bar and the scrolling region; `BackgroundTaskList` renders the rows and
 * `GoalCard` the goal.
 *
 * The goal appears on TWO tabs, for different jobs. On **All** it sits above the
 * rows, because "everything this agent is working on" includes the objective it
 * is working toward. On **Goal** it is alone with its controls, and its empty
 * state is where a goal gets set -- which is why that tab is always present
 * rather than appearing only when a goal exists.
 */
export const AgentWorkPanel: Component<AgentWorkPanelProps> = (props) => {
  // Per mount, not shared: the sidebar section and the popover are separate
  // mounts, and a tab one of them picked is not a preference for the other.
  const [tab, setTab] = createSignal<AgentWorkTabKey>('all')
  // Ties each role=tab to the region it swaps. Unique per mount, because both
  // surfaces can be on screen at once and an id may name only one element.
  const panelId = createUniqueId()

  const showsGoal = () => tab() === 'goal' || tab() === 'all'

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
        tabs={TABS}
        active={tab()}
        onSelect={setTab}
        ariaLabel="Filter agent work"
        panelId={panelId}
        testId="bg-task-filter-tab-bar"
        tabTestId={key => `bg-task-filter-${key}`}
      />
      {/* `tabIndex`, because `rows` is the scroller for both surfaces and holds
          nothing focusable of its own: the rows are buttons only when a
          subagent can be opened. Without it a keyboard user cannot reach a
          registry taller than the box -- the arrow keys land on the tablist,
          which spends them switching tabs. The Files section's panel carries
          the same attribute for the same reason. */}
      <div id={panelId} role="tabpanel" tabIndex={0} class={styles.rows}>
        <Show when={showsGoal()}>
          <GoalCard
            goal={props.goal}
            progress={props.goalProgress}
            supportedActions={props.goalActions}
            onAction={props.onGoalAction}
          />
        </Show>
        <Show when={taskKindFor(tab())}>
          {kind => (
            <BackgroundTaskList
              tasks={props.tasks}
              kind={kind()}
              emptyMessage={TABS_META[tab()].empty}
              loadFailed={props.loadFailed}
              onOpenSubagent={props.onOpenSubagent}
            />
          )}
        </Show>
      </div>
    </div>
  )
}
