import type { TodoItem } from '~/models/todo'
import { isObject } from '~/lib/jsonPick'
import { todoRowKey } from '~/models/todo'

export function normalizeTodoStatus(raw: unknown): TodoItem['status'] {
  if (raw === 'completed')
    return 'completed'
  if (raw === 'in_progress' || raw === 'inProgress')
    return 'in_progress'
  if (raw === 'deleted' || raw === 'cancelled' || raw === 'canceled')
    return 'deleted'
  return 'pending'
}

function todoText(value: unknown): string {
  if (value === undefined || value === null)
    return ''
  if (typeof value === 'object' || typeof value === 'function')
    return ''
  return String(value)
}

export function rawTodosToItems(raw: unknown): TodoItem[] {
  if (!Array.isArray(raw))
    return []
  return raw.flatMap((entry, index) => {
    if (!isObject(entry))
      return []
    const content = todoText(entry.content)
    return [{
      rowKey: todoRowKey(undefined, index, content),
      content,
      status: normalizeTodoStatus(entry.status),
      activeForm: todoText(entry.activeForm),
    }]
  })
}
