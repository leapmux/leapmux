import type { TodoItem } from '~/models/todo'
import { KIMI_TODO_STATUS } from '~/generated/contracts/kimi-protocol'
import { isObject, pickString } from '~/lib/jsonPick'
import { todoRowKey } from '~/models/todo'
import { normalizeTodoStatus } from '../../../normalizers/todo'

/**
 * One `TodoList` status word, in the shared status vocabulary.
 *
 * Kimi Code spells a finished item `done`, which the shared normalizer does not know.
 */
function kimiTodoStatus(status: unknown): TodoItem['status'] {
  switch (status) {
    case KIMI_TODO_STATUS.Done:
      return 'completed'
    case KIMI_TODO_STATUS.InProgress:
      return 'in_progress'
    case KIMI_TODO_STATUS.Pending:
      return 'pending'
    default:
      return normalizeTodoStatus(status)
  }
}

/**
 * The items of a `TodoList` call's `todos` argument.
 *
 * Each call states the WHOLE list, so position is the identity of an item: Kimi Code
 * gives an item no id of its own. An item is `{title, status}`.
 */
export function kimiTodoItems(raw: unknown): TodoItem[] {
  if (!Array.isArray(raw))
    return []
  return raw.flatMap((entry, index) => {
    if (!isObject(entry))
      return []
    const content = pickString(entry, 'title')
    return [{ rowKey: todoRowKey(undefined, index, content), content, status: kimiTodoStatus(entry.status), activeForm: '' }]
  })
}
