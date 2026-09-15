import type { JSX } from 'solid-js'
import type { RenderContext } from '../../../messageRenderers'
import { isObject, pickObject } from '~/lib/jsonPick'
import { TodoListMessage } from '../../../todoListMessage'
import { getMessageContentArray } from '../extractors/assistantContent'
import { claudeTodoWriteFromInput } from '../extractors/todo'

/** Render TodoWrite tool_use with a visual todo list. Returns null if input is invalid. */
export function renderTodoWrite(toolUse: Record<string, unknown>, context?: RenderContext): JSX.Element | null {
  const source = claudeTodoWriteFromInput(pickObject(toolUse, 'input'))
  if (!source)
    return null
  const hasResult = () => getMessageContentArray(context?.sources?.result()?.parentObject)?.some(block =>
    isObject(block) && block.type === 'tool_result' && !!toolUse.id && block.tool_use_id === toolUse.id && block.is_error !== true) ?? false
  return <TodoListMessage source={source} showBody={!hasResult()} context={context} />
}
