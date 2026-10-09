import type { Component } from 'solid-js'
import type { TodoItem } from '~/models/todo'
import type { GoalSurface } from '~/stores/chatGoal'
import { Show } from 'solid-js'
import { GoalCard } from '~/components/goal/GoalCard'
import * as styles from './GoalsAndTodos.css'
import { TodoList } from './TodoList'

export interface GoalsAndTodosProps {
  /** The optional goal surface from the host. */
  goal?: GoalSurface
  todos: TodoItem[]
  announceGoal?: boolean
  variant: 'sidebar' | 'popover'
}

/** Render the goal surface above the agent's to-do list. */
export const GoalsAndTodos: Component<GoalsAndTodosProps> = props => (
  <div
    class={styles.root}
    classList={{ [styles.popoverRoot]: props.variant === 'popover' }}
    data-testid="goals-and-todos"
  >
    {/* The host's surface builder requires a current goal or a supported Set action.
        It omits the surface when both are absent.
        A stopped agent's stored goal still supplies a read-only surface. */}
    <Show when={props.goal}>
      {/* The sidebar owns the live region.
          The popover displays the same goal silently. */}
      {goal => (
        <GoalCard
          goal={goal()}
          {...(props.announceGoal !== undefined ? { announce: props.announceGoal } : {})}
        />
      )}
    </Show>
    {/* Display the separator only between a goal surface and a nonempty list. */}
    <Show when={props.goal !== undefined && props.todos.length > 0}>
      <hr class={styles.separator} data-testid="goal-card-separator" />
    </Show>
    {/* The host controls whether an empty list appears.
        Transcript tool cards also render TodoList and can use a different empty state.
        This agent list needs no empty message or action because the agent creates its entries. */}
    <Show when={props.todos.length > 0}>
      <TodoList todos={props.todos} />
    </Show>
  </div>
)
