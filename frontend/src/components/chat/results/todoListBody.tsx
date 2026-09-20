import type { JSX } from 'solid-js'
import type { TodoItem } from '~/models/todo'
import { Show } from 'solid-js'
import { TodoList } from '~/components/todo/TodoList'
import { toolInputSummary } from '../toolStyles.css'

/** Render the checklist or its explicit empty state. */
export function TodoListBody(props: { todos: TodoItem[], emptyText?: string }): JSX.Element {
  return <Show when={props.todos.length > 0} fallback={<div class={toolInputSummary}>{props.emptyText ?? 'To-do list cleared'}</div>}><TodoList todos={props.todos} variant="full" /></Show>
}
