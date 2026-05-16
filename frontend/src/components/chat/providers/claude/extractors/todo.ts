import type { TodoListSource } from '../../../todoListMessage'
import type { TodoItem } from '~/stores/chat.store'
import { isObject, pickString } from '~/lib/jsonPick'
import { pluralize } from '~/lib/plural'
import { normalizeTodoStatus, rawTodosToItems } from '~/stores/chat.store'
import { CLAUDE_TOOL } from '~/types/toolMessages'

/**
 * Build a TodoListSource from a Claude `TodoWrite` tool_use input. Returns
 * null when the input is not a recognizable TodoWrite payload.
 */
export function claudeTodoWriteFromInput(
  input: Record<string, unknown> | null | undefined,
): TodoListSource | null {
  if (!input || typeof input !== 'object')
    return null
  if (!Array.isArray((input as Record<string, unknown>).todos))
    return null

  const todos = rawTodosToItems((input as Record<string, unknown>).todos)
  return {
    toolName: CLAUDE_TOOL.TODO_WRITE,
    title: pluralize(todos.length, 'task'),
    todos,
  }
}

/**
 * Convert one Claude Task* result `task` record to a TodoItem, or null
 * when the record is missing an id. Set `includeDescription` for
 * TaskGet (which carries the long-form description); TaskList's per-
 * task records omit it.
 */
export function claudeTaskToTodoItem(
  task: unknown,
  options: { includeDescription?: boolean } = {},
): TodoItem | null {
  if (!isObject(task))
    return null
  const id = pickString(task, 'id')
  if (!id)
    return null
  const description = options.includeDescription ? pickString(task, 'description') : ''
  return {
    id,
    content: pickString(task, 'subject'),
    status: normalizeTodoStatus(task.status),
    activeForm: '',
    description: description || undefined,
  }
}

/**
 * Convert a Claude TaskList tool_result `tasks[]` to TodoItem[]. Used by
 * the TaskList result-side renderer to feed the shared TodoListMessage.
 */
export function claudeTaskListToTodos(tasks: unknown[]): TodoItem[] {
  return tasks.flatMap((t) => {
    const item = claudeTaskToTodoItem(t)
    return item ? [item] : []
  })
}
