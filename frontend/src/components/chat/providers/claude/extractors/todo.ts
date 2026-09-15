import type { TodoListSource } from '../../../todoListMessage'
import { pluralize } from '~/lib/plural'
import { rawTodosToItems } from '~/stores/chatTodos'
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

/** The result supplies the saved list. The matching request supplies omitted items. */
export function claudeTodoWriteFromResult(
  result: Record<string, unknown> | undefined,
  input: Record<string, unknown> | undefined,
): TodoListSource | null {
  return claudeTodoWriteFromInput(Array.isArray(result?.newTodos) ? { todos: result.newTodos } : input)
}
