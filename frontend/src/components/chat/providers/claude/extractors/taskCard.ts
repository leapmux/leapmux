import type { TodoItem } from '~/models/todo'
import { normalizeTodoStatus } from '~/components/chat/normalizers/todo'
import { pickObject, pickString } from '~/lib/jsonPick'
import { todoRowKey } from '~/models/todo'

/**
 * One Claude `Task*` call as a single-item checklist.
 *
 * The three tools act on ONE task each, and a reader wants the same two things
 * from all of them: the state, and what the task says. That is a to-do row --
 * the checkbox, the strike-through on a finished one, the description under the
 * label -- so they draw through the shared checklist body rather than a card of
 * their own. Each of the three answers from a different half of the span, which
 * is why they stay three builders:
 *
 *   - `TaskCreate` states the whole task in its INPUT, before an id exists.
 *   - `TaskUpdate` sends a patch, so the persisted post-update snapshot supplies
 *     the complete task.
 *   - `TaskGet` sends an id alone, so the task comes from the paired result.
 */
export function buildTaskCreateItem(
  toolUseInput: Record<string, unknown> | null | undefined,
  toolUseResult: Record<string, unknown> | null | undefined,
): TodoItem {
  const taskFromResult = pickObject(toolUseResult, 'task')
  const content = pickString(toolUseInput, 'subject') || pickString(taskFromResult, 'subject') || 'New task'
  const id = pickString(taskFromResult, 'task_id') || pickString(taskFromResult, 'id') || undefined
  const description = pickString(toolUseInput, 'description')
  return {
    // The id and the description ride only when the record stated them; a task
    // before its answer carries neither.
    ...(id !== undefined ? { id } : {}),
    rowKey: todoRowKey(id, 0, content),
    content,
    status: 'pending',
    activeForm: pickString(toolUseInput, 'activeForm'),
    ...(description ? { description } : {}),
  }
}

/**
 * The to-do a `TaskUpdate` leaves behind, or null when the patch identifies no task.
 *
 * The worker stores the complete post-update snapshot on the message. A status-only
 * patch therefore keeps the subject, active form, and description that existed at
 * that revision. Rendering never reads the current to-do store.
 */
export function buildTaskUpdateItem(
  snapshot: TodoItem | undefined,
): TodoItem | null {
  return snapshot ?? null
}

/**
 * The to-do a `TaskGet` read back, or null until its result lands.
 *
 * The input is an id alone, so there is nothing to draw before the answer
 * arrives -- and a bubble that appeared with a placeholder and then changed
 * would re-measure the row.
 */
export function buildTaskGetItem(
  toolUseResult: Record<string, unknown> | null | undefined,
): TodoItem | null {
  const task = pickObject(toolUseResult, 'task')
  if (!task)
    return null
  const content = pickString(task, 'subject')
  if (!content)
    return null
  const id = pickString(task, 'task_id') || pickString(task, 'id') || undefined
  const description = pickString(task, 'description')
  return {
    // The id and the description ride only when the record stated them.
    ...(id !== undefined ? { id } : {}),
    rowKey: todoRowKey(id, 0, content),
    content,
    status: normalizeTodoStatus(task.status),
    activeForm: pickString(task, 'activeForm'),
    ...(description ? { description } : {}),
  }
}
