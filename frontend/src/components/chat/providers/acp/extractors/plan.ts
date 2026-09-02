import type { TodoListSource } from '../../../todoListMessage'
import type { TodoItem } from '~/stores/chatTodos'
import { normalizeTodoStatus, todoRowKey } from '~/stores/chatTodos'

interface AcpPlanEntry {
  priority?: string
  status?: string
  content?: string
}

/**
 * Build a TodoListSource from ACP `plan` entries (OpenCode/Goose). Returns
 * null when entries is missing/empty so the caller can render the
 * "cleared" placeholder explicitly.
 */
export function acpPlanFromEntries(
  entries: AcpPlanEntry[] | null | undefined,
): TodoListSource | null {
  if (!entries)
    return null

  const todos: TodoItem[] = entries.map((e, i) => {
    const content = e?.content ?? ''
    return {
      // ACP sends a whole plan with no ids, so position plus content is the
      // identity -- see TodoItem.rowKey.
      rowKey: todoRowKey(undefined, i, content),
      content,
      status: normalizeTodoStatus(e?.status),
      activeForm: '',
    }
  })

  return {
    toolName: 'Plan',
    title: 'Plan',
    todos,
  }
}
