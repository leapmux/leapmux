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
 * when the record is missing an id. Only TaskList feeds this today
 * (multi-row card), and TaskList's per-task records omit the long-
 * form description, so we don't include it.
 */
function claudeTaskToTodoItem(task: unknown): TodoItem | null {
  if (!isObject(task))
    return null
  const id = pickString(task, 'id')
  if (!id)
    return null
  return {
    id,
    content: pickString(task, 'subject'),
    status: normalizeTodoStatus(task.status),
    activeForm: '',
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

/**
 * Build the TodoListSource for a Claude TaskList tool_use from its
 * paired `tool_use_result.tasks`. Returns null when the result hasn't
 * arrived (pre-resolve renders nothing).
 */
export function buildTaskListSource(
  toolUseResult: Record<string, unknown> | null | undefined,
): TodoListSource | null {
  if (!toolUseResult || !Array.isArray(toolUseResult.tasks))
    return null
  const todos = claudeTaskListToTodos(toolUseResult.tasks)
  return {
    toolName: CLAUDE_TOOL.TASK_LIST,
    title: pluralize(todos.length, 'task'),
    todos,
  }
}
