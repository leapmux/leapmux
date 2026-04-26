import type { TodoListSource } from '../../../todoListMessage'
import type { TodoItem } from '~/stores/chat.store'
import { pluralize } from '~/lib/plural'

interface RawTodoInput {
  todos?: Array<Record<string, unknown>>
}

/**
 * Build a TodoListSource from a Claude `TodoWrite` tool_use input. Returns
 * null when the input is not a recognizable TodoWrite payload.
 */
export function claudeTodoWriteFromInput(
  input: Record<string, unknown> | null | undefined,
): TodoListSource | null {
  if (!input || typeof input !== 'object')
    return null
  const todosRaw = (input as RawTodoInput).todos
  if (!Array.isArray(todosRaw))
    return null

  const todos: TodoItem[] = todosRaw.map(t => ({
    content: String(t.content || ''),
    status: t.status === 'in_progress'
      ? 'in_progress'
      : t.status === 'completed'
        ? 'completed'
        : 'pending',
    activeForm: String(t.activeForm || ''),
  }))

  return {
    toolName: 'TodoWrite',
    title: pluralize(todos.length, 'task'),
    todos,
  }
}
