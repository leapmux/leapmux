import type { Component } from 'solid-js'
import type { TodoItem } from '~/stores/chatTodos'
import { createMemo, For } from 'solid-js'
import { ClippedText } from '~/components/common/ClippedText'
import { isFinishedTodoStatus, sortTodos, todoDisplayLabel } from '~/stores/chatTodos'
import { TaskCheckbox } from './TaskCheckbox'
import * as styles from './TodoList.css'

interface TodoListProps {
  todos: TodoItem[]
  /**
   * How much room the surface has.
   *
   * `compact` (the default) holds each label to one clipped line, which is what
   * the sidebar section and the ThinkingIndicator popover have room for.
   * `full` lets the label wrap, for the chat transcript's tool card, which
   * stretches to the tile and can show the whole to-do.
   */
  variant?: 'compact' | 'full'
}

export const TodoList: Component<TodoListProps> = (props) => {
  // Sorted HERE rather than at each call site, so the sidebar section, the
  // ThinkingIndicator popover, and the chat cards all read in the same order.
  const ordered = createMemo(() => sortTodos(props.todos))
  return (
    <div
      class={styles.todoList}
      classList={{ [styles.todoListWrapping]: props.variant === 'full' }}
    >
      <For each={ordered()}>
        {(todo) => {
          // Accessors, not hoisted values. A to-do is a Solid store proxy, so
          // these reads have to stay inside a tracked scope: a plain `const`
          // here would freeze the label while the sibling `classList` below
          // kept tracking the same status.
          const label = () => todoDisplayLabel(todo)
          const description = () => todo.description
          // The compact list (sidebar + TodoWrite / Codex / OpenCode chat
          // cards) doesn't have room for a description line, so surface it in
          // the tooltip under the label it explains. That tooltip shows even
          // while the label fits, because the description is the part the row
          // cannot show at all.
          //
          // The hover target is the LABEL, not the whole row: the tooltip has
          // to hang off the clipped element for `showWhen` to measure it.
          return (
            <div
              class={styles.todoItem}
              classList={{
                [styles.todoStruck]: isFinishedTodoStatus(todo.status),
                [styles.todoInProgress]: todo.status === 'in_progress',
              }}
            >
              <div class={styles.todoIcon}>
                <TaskCheckbox status={todo.status} />
              </div>
              <ClippedText
                text={label()}
                class={styles.todoText}
                detail={description()}
              />
            </div>
          )
        }}
      </For>
    </div>
  )
}
