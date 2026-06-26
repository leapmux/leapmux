import type { Component } from 'solid-js'
import type { TodoItem } from '~/stores/chatTodos'
import { For } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { isTerminalTodoStatus, todoDisplayLabel } from '~/stores/chatTodos'
import { TaskCheckbox } from './TaskCheckbox'
import * as styles from './TodoList.css'

interface TodoListProps {
  todos: TodoItem[]
}

export const TodoList: Component<TodoListProps> = (props) => {
  return (
    <div class={styles.todoList}>
      <For each={props.todos}>
        {(todo) => {
          const row = (
            <div
              class={styles.todoItem}
              classList={{
                [styles.todoStruck]: isTerminalTodoStatus(todo.status),
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
