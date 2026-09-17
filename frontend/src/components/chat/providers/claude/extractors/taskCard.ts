import type { TodoItem } from '~/models/todo'
import { pickObject, pickString } from '~/lib/jsonPick'
import { normalizeTodoStatus, todoRowKey } from '~/models/todo'

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
 *   - `TaskUpdate` sends a PATCH, so the unchanged fields come from the live
 *     to-do store and the authoritative status from the paired result.
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
 * `getTodoById` reads the live store, which holds the fields a status-only patch
 * omits. Without it a card that moved a task to `completed` showed `Task #<id>`
 * where the subject belongs. An update that carries no status at all keeps the
 * stored one, so a metadata-only patch does not flip a finished row back to
 * pending.
 */
export function buildTaskUpdateItem(
  toolUseInput: Record<string, unknown> | null | undefined,
  toolUseResult: Record<string, unknown> | null | undefined,
  getTodoById?: (taskId: string) => TodoItem | undefined,
): TodoItem | null {
  const taskId = pickString(toolUseInput, 'taskId') || pickString(toolUseResult, 'taskId')
  if (!taskId)
    return null

  const stored = getTodoById?.(taskId)
  const content = pickString(toolUseInput, 'subject') || stored?.content || `Task #${taskId}`
  const statusChange = pickObject(toolUseResult, 'statusChange')
  const rawStatus = pickString(statusChange, 'to') || pickString(toolUseInput, 'status')
  const description = pickString(toolUseInput, 'description') || stored?.description
  return {
    id: taskId,
    rowKey: todoRowKey(taskId, 0, content),
    content,
    status: rawStatus ? normalizeTodoStatus(rawStatus) : (stored?.status ?? 'pending'),
    activeForm: pickString(toolUseInput, 'activeForm') || stored?.activeForm || '',
    // The description rides only when the patch or the store stated one.
    ...(description !== undefined ? { description } : {}),
  }
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
