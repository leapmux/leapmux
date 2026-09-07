import type { Component } from 'solid-js'
import type { FilterTab } from '~/components/common/FilterTabBar'
import type { BackgroundTaskItem, BackgroundTaskKindFilter } from '~/stores/chatBackgroundTasks'
import type { GoalSurface } from '~/stores/chatGoal'
import { createMemo, createSignal, createUniqueId, Show } from 'solid-js'
import { FilterTabBar } from '~/components/common/FilterTabBar'
import { hasGoalSurface } from '~/stores/chatGoal'
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
const LIST_TABS_META: Record<BackgroundTaskKindFilter, { label: string, empty: string }> = {
  all: { label: 'All', empty: 'No background tasks' },
  subagent: { label: 'Subagents', empty: 'No subagents' },
  shell: { label: 'Shell', empty: 'No shell commands' },
}

/**
 * The Goal tab, declared apart because it holds no LIST.
 *
 * It carries no `empty` message, and that absence is the point: the goal tab
 * renders a GoalCard, which states its own empty case, so a message declared
 * here would never render. Folding the goal into the Record above forced one
 * anyway, and forced the key union to widen and then narrow back out again.
 */
const GOAL_TAB = { key: 'goal', label: 'Goal' } as const

/**
 * Which registry kinds a tab shows, or undefined for the goal tab.
 *
 * This is the one place the panel's key union narrows to the registry's, so
 * `filterBackgroundTasksByKind` is never called with a key it does not know.
 */
function listTabFor(tab: AgentWorkTabKey): BackgroundTaskKindFilter | undefined {
  return tab === GOAL_TAB.key ? undefined : tab
}

export interface AgentWorkPanelProps {
  tasks: BackgroundTaskItem[]
  /** The goal, its counters, the live actions and their handler. */
  goal: GoalSurface
  /**
   * The worker could not answer for this registry, so an empty list means "no
   * answer", not "no tasks". Says so in place of the empty message: the two are
   * otherwise indistinguishable, and the section is hidden when it is empty, so
   * a failure that reads as emptiness disappears entirely.
   */
  loadFailed?: boolean
  onOpenSubagent?: (item: BackgroundTaskItem) => void
  /**
   * Whether this panel's GoalCard owns the live region. Exactly one instance
   * announces; see GoalCardProps.announce.
   */
  announceGoal?: boolean
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

  // The goal surface exists when there is a goal to show, or when this agent
  // can be given one. Several providers can do neither -- OpenCode, Kilo and Pi
  // have no goal feature at all, and LeapMux does not yet read the ones Goose
  // and Cursor do have -- and for them an always-present Goal tab could only
  // ever say "No session goal" and the All tab would carry a dead card above
  // the rows.
  //
  // The shell asks the SAME question to decide whether the section is visible
  // at all, through the same helper, so the two answers cannot disagree.
  const hasGoal = () => hasGoalSurface(props.goal)

  const tabs = createMemo<readonly FilterTab<AgentWorkTabKey>[]>(() => {
    const listTabs = (Object.keys(LIST_TABS_META) as BackgroundTaskKindFilter[])
      .map(key => ({ key, label: LIST_TABS_META[key].label }))
    return hasGoal() ? [...listTabs, GOAL_TAB] : listTabs
  })

  // The ACTIVE tab is derived, never stored, so a selection the tab list no
  // longer offers is unrepresentable rather than repaired afterwards.
  //
  // The Goal tab can disappear -- an agent's process exits and its capability
  // list empties. A `createEffect` that wrote 'all' back would leave one
  // committed render where `tab()` names a key `tabs()` does not contain, and
  // FilterTabBar gives every tab `tabIndex={-1}` and `aria-selected={false}` in
  // that state: a tablist with no selected tab. Deriving skips that state.
  //
  // The selection RETURNS to Goal when the tab does, which is what a user who
  // was reading the goal expects after a relaunch.
  const activeTab = () => (tabs().some(t => t.key === tab()) ? tab() : 'all')

  const showsGoal = () => hasGoal() && (activeTab() === GOAL_TAB.key || activeTab() === 'all')

  // The rule under the goal, on the one tab where something follows it. The
  // card draws no separator of its own, because it cannot see what is below.
  const showsGoalSeparator = () => showsGoal() && activeTab() === 'all'

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
        tabs={tabs()}
        active={activeTab()}
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
            announce={props.announceGoal}
          />
        </Show>
        <Show when={showsGoalSeparator()}>
          <hr class={styles.goalSeparator} data-testid="goal-separator" />
        </Show>
        <Show when={listTabFor(activeTab())}>
          {kind => (
            <BackgroundTaskList
              tasks={props.tasks}
              kind={kind()}
              emptyMessage={LIST_TABS_META[kind()].empty}
              loadFailed={props.loadFailed}
              onOpenSubagent={props.onOpenSubagent}
            />
          )}
        </Show>
      </div>
    </div>
  )
}
