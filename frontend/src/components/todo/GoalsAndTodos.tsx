import type { Component } from 'solid-js'
import type { GoalSurface } from '~/stores/chatGoal'
import type { TodoItem } from '~/stores/chatTodos'
import { Show } from 'solid-js'
import { GoalCard } from '~/components/backgroundtasks/GoalCard'
import * as styles from './GoalsAndTodos.css'
import { TodoList } from './TodoList'

export interface GoalsAndTodosProps {
  /** Absent when the provider has no session-goal feature. */
  goal?: GoalSurface
  todos: TodoItem[]
  announceGoal?: boolean
  variant: 'sidebar' | 'popover'
}

/** Renders the session goal above the agent's to-do list. */
export const GoalsAndTodos: Component<GoalsAndTodosProps> = props => (
  <div
    class={styles.root}
    classList={{ [styles.popoverRoot]: props.variant === 'popover' }}
    data-testid="goals-and-todos"
  >
    {/* The provider decides whether the card exists. A stopped process can
        report no goal and no actions, but the empty card still explains the
        provider feature. */}
    <Show when={props.goal}>
      {/* The sidebar owns the live region. The popover renders the same goal
          silently, so one update causes one announcement. */}
      {goal => <GoalCard goal={goal()} announce={props.announceGoal} />}
    </Show>
    {/* The rule appears only when it separates the card from a list. */}
    <Show when={props.goal !== undefined && props.todos.length > 0}>
      <hr class={styles.separator} data-testid="goal-card-separator" />
    </Show>
    {/* The host controls emptiness because transcript tool cards also render
        TodoList. An empty agent list needs no message or action, because the
        agent creates this list. */}
    <Show when={props.todos.length > 0}>
      <TodoList todos={props.todos} />
    </Show>
  </div>
)
