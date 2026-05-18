import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import { pickObject } from '~/lib/jsonPick'
import { TaskCardMessage } from '../../../taskCardMessage'
import { TodoListMessage } from '../../../todoListMessage'
import {
  buildTaskCreateSource,
  buildTaskGetSource,
  buildTaskUpdateSource,
  readToolUseResult,
} from '../extractors/taskCard'
import { buildTaskListSource } from '../extractors/todo'

/**
 * Render a Claude TaskCreate tool_use as a single-row card. Subject
 * surfaces from the input (always present); description, if any,
 * becomes the summary line below.
 */
export function renderTaskCreate(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const input = pickObject(toolUse, 'input')
  if (!input)
    return null
  const source = buildTaskCreateSource(input, readToolUseResult(context?.toolResultParsed))
  return <TaskCardMessage source={source} context={context} />
}

/**
 * Render a Claude TaskUpdate tool_use as a single-row card. Pulls the
 * authoritative final status from the paired tool_result when present;
 * the input's status is used as a fallback so unresolved requests
 * still paint a meaningful state. Subject falls back to the live todos
 * store when the patch omits it (status-only updates) so the card
 * still says something more useful than "Task #ID".
 */
export function renderTaskUpdate(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const input = pickObject(toolUse, 'input')
  const source = buildTaskUpdateSource(input, readToolUseResult(context?.toolResultParsed), context?.getTodoById)
  if (!source)
    return null
  return <TaskCardMessage source={source} context={context} />
}

/**
 * Render a Claude TaskGet tool_use as a single-row card. The input is
 * empty; data lives in the paired tool_use_result. Returns null until
 * the result arrives (no flicker bubble pre-resolve).
 */
export function renderTaskGet(context?: RenderContext): JSX.Element | null {
  const source = buildTaskGetSource(readToolUseResult(context?.toolResultParsed))
  if (!source)
    return null
  return <TaskCardMessage source={source} context={context} />
}

/**
 * Render a Claude TaskList tool_use as the multi-row TodoListMessage
 * ("N tasks" header + per-task rows). The input is empty; the tasks
 * array lives in the paired tool_use_result. Mirrors the layout that
 * the result-side renderer produced before this rework.
 */
export function renderTaskList(context?: RenderContext): JSX.Element | null {
  const source = buildTaskListSource(readToolUseResult(context?.toolResultParsed))
  if (!source)
    return null
  return <TodoListMessage source={source} context={context} />
}
