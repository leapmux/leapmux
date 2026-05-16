import type { TodoListSource } from '../../../todoListMessage'
import { pickObject, pickString } from '~/lib/jsonPick'
import { normalizeTodoStatus } from '~/stores/chat.store'
import { CLAUDE_TOOL, TASK_DELETE_SENTINEL } from '~/types/toolMessages'

function isTaskDeleteRequest(
  toolUseInput: Record<string, unknown> | null | undefined,
  statusChange: Record<string, unknown> | null,
): boolean {
  return pickString(statusChange, 'to') === TASK_DELETE_SENTINEL
    || pickString(toolUseInput, 'status') === TASK_DELETE_SENTINEL
}

/**
 * Build a TodoListSource for an inline TaskCreate card. Accepts the
 * tool_use input (always present once persisted) and the optional
 * tool_use_result envelope (present after the agent assigns the id).
 * Used from both the tool_use renderer and the tool_result re-render.
 */
export function buildTaskCreateSource(
  toolUseInput: Record<string, unknown> | null | undefined,
  toolUseResult: Record<string, unknown> | null | undefined,
): TodoListSource {
  const taskFromResult = pickObject(toolUseResult, 'task')
  const taskId = pickString(taskFromResult, 'id')
  const subject = pickString(toolUseInput, 'subject') || pickString(taskFromResult, 'subject')
  const description = pickString(toolUseInput, 'description')
  return {
    toolName: CLAUDE_TOOL.TASK_CREATE,
    title: taskId ? `Task #${taskId}` : 'Task created',
    todos: [{
      id: taskId || undefined,
      content: subject,
      status: 'pending',
      activeForm: pickString(toolUseInput, 'activeForm'),
      description: description || undefined,
    }],
    bordered: false,
  }
}

/**
 * Build a TodoListSource for an inline TaskUpdate card. Dispatches to
 * the delete branch when the wire carries the delete sentinel; the
 * regular path funnels status through `normalizeTodoStatus` so
 * `inProgress` / `in_progress` variants normalize uniformly.
 */
export function buildTaskUpdateSource(
  toolUseInput: Record<string, unknown> | null | undefined,
  toolUseResult: Record<string, unknown> | null | undefined,
): TodoListSource | null {
  const taskId = pickString(toolUseInput, 'taskId') || pickString(toolUseResult, 'taskId')
  if (!taskId)
    return null
  const statusChange = pickObject(toolUseResult, 'statusChange')
  if (isTaskDeleteRequest(toolUseInput, statusChange))
    return buildTaskDeleteSource(taskId, toolUseInput)
  return {
    toolName: CLAUDE_TOOL.TASK_UPDATE,
    title: `Task #${taskId} updated`,
    todos: [{
      id: taskId,
      content: pickString(toolUseInput, 'subject') || `Task #${taskId}`,
      status: normalizeTodoStatus(pickString(statusChange, 'to') || pickString(toolUseInput, 'status')),
      activeForm: pickString(toolUseInput, 'activeForm'),
      description: pickString(toolUseInput, 'description') || undefined,
    }],
    bordered: false,
  }
}

function buildTaskDeleteSource(
  taskId: string,
  toolUseInput: Record<string, unknown> | null | undefined,
): TodoListSource {
  return {
    toolName: CLAUDE_TOOL.TASK_UPDATE,
    title: `Task #${taskId} deleted`,
    todos: [{
      id: taskId,
      content: pickString(toolUseInput, 'subject') || `Task #${taskId}`,
      status: 'completed',
      activeForm: '',
    }],
    bordered: false,
  }
}

/** Pull the paired tool_result's `tool_use_result` envelope, if any. */
export function readToolUseResult(parsed: { parentObject?: Record<string, unknown> } | null | undefined): Record<string, unknown> | null {
  return pickObject(parsed?.parentObject, 'tool_use_result')
}
