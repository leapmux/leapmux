import type { TaskCardSource } from '../../../taskCardMessage'
import type { TodoItem } from '~/stores/chat.store'
import { pickObject, pickString } from '~/lib/jsonPick'
import { normalizeTodoStatus } from '~/stores/chat.store'
import { CLAUDE_TOOL } from '~/types/toolMessages'

/**
 * Build a TaskCardSource for an inline TaskCreate card. Accepts the
 * tool_use input (always present once persisted) and the optional
 * tool_use_result envelope (present after the agent assigns the id).
 */
export function buildTaskCreateSource(
  toolUseInput: Record<string, unknown> | null | undefined,
  toolUseResult: Record<string, unknown> | null | undefined,
): TaskCardSource {
  const taskFromResult = pickObject(toolUseResult, 'task')
  const subject = pickString(toolUseInput, 'subject') || pickString(taskFromResult, 'subject') || 'New task'
  const description = pickString(toolUseInput, 'description')
  return {
    toolName: CLAUDE_TOOL.TASK_CREATE,
    subject,
    description: description || undefined,
    status: 'pending',
    activeForm: pickString(toolUseInput, 'activeForm') || undefined,
  }
}

/**
 * Build a TaskCardSource for an inline TaskUpdate card. Status flows
 * through `normalizeTodoStatus` (which canonicalizes `'deleted'` like
 * any other state). Falls back to the live todos store (via
 * `getTodoById`) for `subject` / `description` / `activeForm` on
 * status-only patches.
 */
export function buildTaskUpdateSource(
  toolUseInput: Record<string, unknown> | null | undefined,
  toolUseResult: Record<string, unknown> | null | undefined,
  getTodoById?: (taskId: string) => TodoItem | undefined,
): TaskCardSource | null {
  const taskId = pickString(toolUseInput, 'taskId') || pickString(toolUseResult, 'taskId')
  if (!taskId)
    return null

  const stored = getTodoById?.(taskId)
  const subject = pickString(toolUseInput, 'subject')
    || stored?.content
    || `Task #${taskId}`
  const description = pickString(toolUseInput, 'description')
    || stored?.description
    || ''
  const statusChange = pickObject(toolUseResult, 'statusChange')
  const rawStatus = pickString(statusChange, 'to') || pickString(toolUseInput, 'status')
  // No status info on this patch (metadata-only update): preserve the
  // stored status so the card doesn't flip a completed/in_progress row
  // back to pending.
  const status = rawStatus ? normalizeTodoStatus(rawStatus) : (stored?.status ?? 'pending')

  return {
    toolName: CLAUDE_TOOL.TASK_UPDATE,
    subject,
    description: description || undefined,
    status,
    activeForm: pickString(toolUseInput, 'activeForm') || stored?.activeForm || undefined,
  }
}

/**
 * Build a TaskCardSource for a TaskGet card. TaskGet's input is
 * empty; the data lives in `tool_use_result.task`. Returns null when
 * the result hasn't arrived yet (pre-resolve renders nothing).
 */
export function buildTaskGetSource(
  toolUseResult: Record<string, unknown> | null | undefined,
): TaskCardSource | null {
  const task = pickObject(toolUseResult, 'task')
  if (!task)
    return null
  const subject = pickString(task, 'subject')
  if (!subject)
    return null
  const description = pickString(task, 'description')
  return {
    toolName: CLAUDE_TOOL.TASK_GET,
    subject,
    description: description || undefined,
    status: normalizeTodoStatus(task.status),
  }
}

/** Pull the paired tool_result's `tool_use_result` envelope, if any. */
export function readToolUseResult(parsed: { parentObject?: Record<string, unknown> } | null | undefined): Record<string, unknown> | null {
  return pickObject(parsed?.parentObject, 'tool_use_result')
}
