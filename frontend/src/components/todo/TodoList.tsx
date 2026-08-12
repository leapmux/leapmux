import type { Component } from 'solid-js'
import type { TodoItem } from '~/stores/chatTodos'
import { createMemo, For } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { isFinishedTodoStatus, sortTodos, todoDisplayLabel } from '~/stores/chatTodos'
import { TaskCheckbox } from './TaskCheckbox'
import * as styles from './TodoList.css'

interface TodoListProps {
  todos: TodoItem[]
}

export const TodoList: Component<TodoListProps> = (props) => {
  // Sorted HERE rather than at each call site, so the sidebar section, the
  // ThinkingIndicator popover, and the chat cards all read in the same order.
  const ordered = createMemo(() => sortTodos(props.todos))
  return (
    <div class={styles.todoList}>
      <For each={ordered()}>
        {(todo) => {
          const row = (
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
              <span class={styles.todoText}>{todoDisplayLabel(todo)}</span>
            </div>
          )
          // The compact list (sidebar + TodoWrite / Codex / OpenCode
          // chat cards) doesn't have room for a description line, so
          // surface it via the Tooltip component when present.
          return todo.description
            ? <Tooltip text={todo.description}>{row}</Tooltip>
            : row
        }}
      </For>
    </div>
  )
}
