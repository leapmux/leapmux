import type { TodoItem } from '~/models/todo'
import { rawTodosToItems } from '~/components/chat/normalizers/todo'
import { CODEWHALE_RESULT_FIELD, CODEWHALE_TOOL } from '~/generated/contracts/codewhale-protocol'
import { isObject, pickObject, pickString } from '~/lib/jsonPick'

/**
 * The checklist one to-do call states, read from its arguments.
 *
 * `todo_write` and its hidden aliases send `{todos:[{content, status}]}`, the whole
 * list each time. `update_plan` sends its steps as `{plan:[{step, status}]}`. Null when
 * the arguments hold neither list, which makes the row take the generic card rather
 * than draw an empty checklist.
 *
 * The ITEMS alone. The header above them belongs to the shared to-do renderer.
 */
export function codewhaleTodoItems(toolName: string, args: Record<string, unknown>): TodoItem[] | null {
  if (Array.isArray(args.todos))
    return rawTodosToItems(args.todos)
  if (toolName === CODEWHALE_TOOL.UpdatePlan && Array.isArray(args.plan)) {
    return rawTodosToItems(args.plan.filter(isObject).map(step => ({
      content: pickString(step, 'step'),
      status: pickString(step, 'status'),
    })))
  }
  return null
}

/** The explanation an `update_plan` call gives above its steps, when it gives one. */
export function codewhalePlanNote(toolName: string, args: Record<string, unknown>): string {
  return toolName === CODEWHALE_TOOL.UpdatePlan ? pickString(args, 'explanation').trim() : ''
}

/**
 * The checklist the runtime KEPT after a `todo_write` call, from the result's
 * `metadata.task_updates.checklist.items`.
 *
 * The runtime numbers the items and normalizes their status, so this is the list the
 * reader should see once the call lands. Null when the result carries no checklist.
 */
export function codewhaleChecklistItems(metadata: Record<string, unknown>): TodoItem[] | null {
  const checklist = pickObject(pickObject(metadata, CODEWHALE_RESULT_FIELD.TaskUpdates), CODEWHALE_RESULT_FIELD.Checklist)
  const items = checklist?.[CODEWHALE_RESULT_FIELD.Items]
  return Array.isArray(items) ? rawTodosToItems(items) : null
}
