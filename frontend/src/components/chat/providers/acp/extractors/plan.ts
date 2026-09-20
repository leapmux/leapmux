import type { TodoItem } from '~/models/todo'
import { normalizeTodoStatus } from '~/components/chat/normalizers/todo'
import { isObject, pickString } from '~/lib/jsonPick'
import { todoRowKey } from '~/models/todo'

/**
 * The checklist items an ACP `plan` update carries, or null when it carries none.
 *
 * Null rather than an empty list: a plan with no entries at all is a frame the row
 * cannot draw, while an empty ARRAY is a cleared list, which the shared checklist
 * header states in its own words.
 *
 * Each ENTRY is read, not asserted. An `Array.isArray` says nothing about the
 * elements, and an entry that is no object states neither content nor status -- so it
 * is dropped rather than drawn as a blank checklist row. The index stays the entry's
 * place in the RAW plan, which is what keeps `rowKey` stable across a re-send even
 * where a dropped entry makes it differ from the output index.
 */
export function acpPlanTodos(entries: unknown): TodoItem[] | null {
  if (!Array.isArray(entries))
    return null
  return entries.flatMap((entry, index) => {
    if (!isObject(entry))
      return []
    const content = pickString(entry, 'content')
    return [{
      // ACP sends a whole plan with no ids, so position plus content is the
      // identity -- see TodoItem.rowKey.
      rowKey: todoRowKey(undefined, index, content),
      content,
      status: normalizeTodoStatus(entry.status),
      activeForm: '',
    }]
  })
}
