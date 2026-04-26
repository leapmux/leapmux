import type { TodoListSource } from '../../../todoListMessage'
import type { TodoItem } from '~/stores/chat.store'
import { normalizeTodoStatus } from '~/stores/chat.store'

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

  const todos: TodoItem[] = entries.map(e => ({
    content: e?.content ?? '',
    status: normalizeTodoStatus(e?.status),
    activeForm: '',
  }))

  return {
    toolName: 'Plan',
    title: 'Plan',
    todos,
  }
}
