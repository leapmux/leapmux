import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import { isObject } from '~/lib/jsonPick'
import { TodoListMessage } from '../../../todoListMessage'
import { claudeTodoWriteFromInput } from '../extractors/todo'

/** Render TodoWrite tool_use with a visual todo list. Returns null if input is invalid. */
export function renderTodoWrite(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const input = isObject(toolUse.input) ? toolUse.input as Record<string, unknown> : null
  const source = claudeTodoWriteFromInput(input)
  if (!source)
    return null
  return <TodoListMessage source={source} context={context} />
}
