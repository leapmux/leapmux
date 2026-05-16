import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import { pickObject } from '~/lib/jsonPick'
import { TodoListMessage } from '../../../todoListMessage'
import { buildTaskCreateSource, buildTaskUpdateSource, readToolUseResult } from '../extractors/taskCard'

/**
 * Render a TaskCreate tool_use as a single-row card matching one row
 * of the sidebar TodoList. When the paired tool_result is available
 * (post-resolve), the title surfaces the agent-assigned `task.id`.
 */
export function renderTaskCreate(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const input = pickObject(toolUse, 'input')
  if (!input)
    return null
  return <TodoListMessage source={buildTaskCreateSource(input, readToolUseResult(context?.toolResultParsed))} context={context} />
}

/**
 * Render a TaskUpdate tool_use as a single-row card. Pulls the
 * authoritative final status from the paired tool_result when present;
 * the input's status is used as a fallback so unresolved requests still
 * paint a meaningful state.
 */
export function renderTaskUpdate(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const input = pickObject(toolUse, 'input')
  const source = buildTaskUpdateSource(input, readToolUseResult(context?.toolResultParsed))
  if (!source)
    return null
  return <TodoListMessage source={source} context={context} />
}
